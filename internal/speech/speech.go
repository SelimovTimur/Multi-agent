// Package speech — голосовой ввод ученика: распознавание речи и синтез
// ответов аватара.
//
// Схемы базы у модуля нет: он ничего не хранит. Его задача — провести
// тяжёлые операции через пулы ML-шлюза и вернуть текст или звук.
//
// Клиенты сервисов (ASR, ffmpeg, синтез) лежат в internal/service.
package speech

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/model"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/google/uuid"
)

// Recognizer — распознавание речи. language — язык урока; пустая строка —
// язык из настройки ASR.
type Recognizer interface {
	// Recognize — материал учителя; запрос доживает, даже если клиент ушёл.
	Recognize(ctx context.Context, wavPath, language string) (model.ASRResponse, error)
	// RecognizeInteractive — ответ ученика: с уверенностью распознавания,
	// обрывается вместе с уходом клиента.
	RecognizeInteractive(ctx context.Context, wavPath, language string) (model.ASRResponse, error)
}

// Converter — приведение произвольного медиа к WAV 16 кГц моно.
type Converter interface {
	ConvertToWav16k(ctx context.Context, inputPath, outputPath string) error
}

// Synthesizer — синтез речи.
type Synthesizer interface {
	Synthesize(ctx context.Context, text, style string) ([]byte, error)
}

// VoiceStyle — тон разговора, заданный учителем (NR-29).
//
// Модуль speech не знает ни про уроки, ни про попытки: он спрашивает тон по
// попытке через интерфейс, а кто и как его хранит — дело learning и content.
// Без подключённого порта синтез звучит нейтрально, как раньше.
type VoiceStyle interface {
	StyleForAttempt(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (string, error)
}

// Service — голосовые операции под контролем пулов.
type Service struct {
	asr    Recognizer
	audio  Converter
	tts    Synthesizer
	gw     *mlgw.Gateway
	tmpDir string

	// Тон разговора подключается отдельно (WithVoiceStyle): без него синтез
	// работает, просто звучит нейтрально.
	style VoiceStyle
}

// WithVoiceStyle подключает тон разговора, заданный учителем.
func (s *Service) WithVoiceStyle(v VoiceStyle) *Service {
	s.style = v
	return s
}

// StyleForAttempt — тон разговора этой попытки. Пустая строка, если тон не
// задан, порт не подключён или попытка чужая: синтез — не то место, где
// стоит ронять урок отказом.
func (s *Service) StyleForAttempt(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) string {
	if s.style == nil || attemptID == uuid.Nil {
		return ""
	}
	style, err := s.style.StyleForAttempt(ctx, actor, attemptID)
	if err != nil {
		return ""
	}
	return style
}

func NewService(asr Recognizer, audio Converter, tts Synthesizer,
	gw *mlgw.Gateway, tmpDir string) *Service {
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	return &Service{asr: asr, audio: audio, tts: tts, gw: gw, tmpDir: tmpDir}
}

// TranscribeResult — что вернуло распознавание.
//
// NeedsReview — распознавание не уверено: ученик видит текст до отправки и
// отправляет его как есть или исправляет (Уч-2). Причины — в ReviewReasons.
type TranscribeResult struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	// Confidence — уверенность 0…1; null, если модель её не даёт.
	Confidence    *float64 `json:"confidence"`
	NeedsReview   bool     `json:"needs_review"`
	ReviewReasons []string `json:"review_reasons"`
}

// reasonNoConfidence — уверенность неизвестна. Та же причина, что ставит
// asr_service для локальной модели.
const reasonNoConfidence = "no_confidence"

// languagePattern — язык урока, как его хранит платформа («tatar»,
// «english»). Значение уходит в подсказку модели распознавания, поэтому
// произвольную строку туда не пропускаем.
var languagePattern = regexp.MustCompile(`^[a-z]{2,16}$`)

// Transcribe распознаёт загруженный файл.
//
// language — язык урока; пустая строка — язык из настройки ASR.
// interactive=true означает, что человек ждёт результат прямо сейчас
// (диктовка в диалоге с аватаром): такой запрос обрывается вместе с
// уходом клиента, а не продолжает занимать ASR.
func (s *Service) Transcribe(ctx context.Context, orgID, userID uuid.UUID,
	src io.Reader, filename, language string, interactive bool) (TranscribeResult, error) {
	if err := checkUpload(filename, language); err != nil {
		return TranscribeResult{}, err
	}
	work, cleanup, err := s.saveUpload(src, filename)
	if err != nil {
		return TranscribeResult{}, err
	}
	defer cleanup()
	return s.recognize(ctx, orgID, userID, work, language, interactive)
}

// checkUpload проверяет имя файла и язык до работы с самим файлом.
//
// Не тот файл — это ошибка человека, а не отказ сервиса. Раньше текстовый
// файл уходил в ffmpeg и возвращался с кодом 503: учитель читал «сервис
// недоступен» и шёл жаловаться, а мониторинг считал это отказом платформы.
func checkUpload(filename, language string) error {
	if err := checkMediaName(filename); err != nil {
		return err
	}
	if language != "" && !languagePattern.MatchString(language) {
		return httpx.FieldError("language", "Неизвестный язык распознавания")
	}
	return nil
}

// recognize перекодирует сохранённую запись и распознаёт её.
func (s *Service) recognize(ctx context.Context, orgID, userID uuid.UUID,
	work, language string, interactive bool) (TranscribeResult, error) {
	wav := work + ".wav"
	defer os.Remove(wav)

	// Конвертация в ffmpeg — тоже нагрузка, но своя: она упирается в CPU,
	// а не в модель. Держим её в медийном пуле, чтобы очередь на ASR не
	// росла из-за перекодирования.
	err := s.gw.Do(ctx, s.call(orgID, userID, "convert", mlgw.PoolMedia),
		func(ctx context.Context) (mlgw.Usage, error) {
			return mlgw.Usage{Provider: "ffmpeg"}, s.audio.ConvertToWav16k(ctx, work, wav)
		})
	if err != nil {
		// ffmpeg отказался читать файл — значит внутри не запись, чем бы ни
		// было написано в имени. Это снова ошибка выбора файла.
		if isConvertFailure(err) {
			return TranscribeResult{}, httpx.FieldError("file",
				"Не похоже на запись. Подойдут mp3, m4a, wav, ogg, mp4, mov и другие аудио и видео")
		}
		return TranscribeResult{}, wrapUpstream(err, "Не удалось подготовить запись")
	}

	var out model.ASRResponse
	stage := "asr"
	if interactive {
		stage = "asr_chat"
	}
	err = s.gw.Do(ctx, s.call(orgID, userID, stage, mlgw.PoolASR),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			switch {
			case interactive:
				out, callErr = s.asr.RecognizeInteractive(ctx, wav, language)
			default:
				out, callErr = s.asr.Recognize(ctx, wav, language)
			}
			return mlgw.Usage{Provider: "asr"}, callErr
		})
	if err != nil {
		return TranscribeResult{}, wrapUpstream(err, "Не удалось распознать речь")
	}
	if out.Error != "" {
		return TranscribeResult{}, httpx.Validation("Не удалось разобрать запись",
			map[string]string{"file": out.Error})
	}
	if strings.TrimSpace(out.Text) == "" {
		return TranscribeResult{}, httpx.Validation("В записи не распознана речь",
			map[string]string{"file": "Проверьте, что на записи есть речь и она достаточно громкая"})
	}
	return transcribeResult(out, interactive), nil
}

// transcribeResult переносит ответ ASR в ответ API.
//
// Ответ ученика (interactive) без уверенности и без решения о проверке
// пришёл от asr_service старой версии. «Не знаем, уверено ли» значит «не
// уверено»: ученик лучше лишний раз нажмёт «Отправить как есть», чем
// получит ошибку, которой не делал. Материал учителя распознаётся без
// уверенности намеренно, и проверка ему не нужна: текст правит учитель.
func transcribeResult(out model.ASRResponse, interactive bool) TranscribeResult {
	res := TranscribeResult{
		Text:          out.Text,
		Language:      out.Language,
		Confidence:    out.Confidence,
		NeedsReview:   out.NeedsReview || len(out.ReviewReasons) > 0,
		ReviewReasons: append([]string{}, out.ReviewReasons...),
	}
	if interactive && res.Confidence == nil && !res.NeedsReview {
		res.NeedsReview = true
		res.ReviewReasons = []string{reasonNoConfidence}
	}
	return res
}

// Synthesize озвучивает текст.
//
// Синтез в отдельном пуле и с низким приоритетом: текст ответа аватара
// ученик получает сразу, а звук догоняет. Если синтез не успел — урок
// продолжается без него (лестница деградации, инвариант И-8).
func (s *Service) Synthesize(ctx context.Context, orgID, userID uuid.UUID, text, style string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, httpx.FieldError("text", "Нечего озвучивать")
	}

	var audio []byte
	err := s.gw.Do(ctx, s.call(orgID, userID, "tts", mlgw.PoolTTS),
		func(ctx context.Context) (mlgw.Usage, error) {
			var callErr error
			audio, callErr = s.tts.Synthesize(ctx, text, style)
			return mlgw.Usage{Provider: "tts"}, callErr
		})
	if err != nil {
		return nil, wrapUpstream(err, "Синтез речи недоступен")
	}
	return audio, nil
}

// saveUpload кладёт загруженный поток во временный файл и отдаёт путь и
// уборку. Уборку зовёт тот, кто получил файл.
//
// ffmpeg работает с файлами, а не с потоками, поэтому обойтись
// без диска не выйдет. Файл удаляется в любом случае — в том числе при
// ошибке на полпути.
func (s *Service) saveUpload(src io.Reader, filename string) (string, func(), error) {
	ext := filepath.Ext(filename)
	if len(ext) > 8 {
		ext = ""
	}
	path := filepath.Join(s.tmpDir, "nr-upload-"+idgen.NewString()+ext)

	f, err := os.Create(path)
	if err != nil {
		return "", func() {}, httpx.Internal("Не удалось сохранить файл").Wrap(err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(path)
	}
	if _, err := io.Copy(f, src); err != nil {
		cleanup()
		return "", func() {}, httpx.Internal("Не удалось сохранить файл").Wrap(err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, httpx.Internal("Не удалось сохранить файл").Wrap(err)
	}
	return path, func() { os.Remove(path) }, nil
}

func (s *Service) call(orgID, userID uuid.UUID, stage, pool string) mlgw.Call {
	return mlgw.Call{
		Pipeline: "speech",
		Stage:    stage,
		Pool:     pool,
		OrgID:    nilable(orgID),
		UserID:   nilable(userID),
	}
}

func nilable(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// wrapUpstream переводит отказ внешнего сервиса в наш конверт.
//
// Отказ шлюза (нет слота в пуле) уже пришёл в нужном виде — не портим его.
// mediaExtensions — расширения, которые ffmpeg точно берёт. Список не для
// защиты (имя файла ничего не доказывает), а для понятного отказа до
// отправки сотни мегабайт на сервер.
var mediaExtensions = map[string]bool{
	".mp3": true, ".m4a": true, ".wav": true, ".ogg": true, ".oga": true, ".opus": true,
	".aac": true, ".flac": true, ".wma": true, ".amr": true,
	".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true, ".3gp": true,
}

func checkMediaName(filename string) error {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	if ext == "" || mediaExtensions[ext] {
		// Без расширения не отказываем: имя файла — не доказательство, а
		// запись из браузера иногда приходит вовсе без него.
		return nil
	}
	return httpx.FieldError("file",
		"Это не аудио и не видео. Подойдут mp3, m4a, wav, ogg, mp4, mov и другие записи")
}

// isConvertFailure — ffmpeg не смог прочитать файл (а не упал сам).
func isConvertFailure(err error) bool {
	var he *httpx.Error
	if asHTTPError(err, &he) {
		return false
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "invalid data") ||
		strings.Contains(low, "does not contain any stream") ||
		strings.Contains(low, "ffmpeg")
}

func wrapUpstream(err error, msg string) error {
	var he *httpx.Error
	if ok := asHTTPError(err, &he); ok {
		return err
	}
	return httpx.Unavailable(msg).Wrap(err)
}

func asHTTPError(err error, target **httpx.Error) bool {
	for err != nil {
		if e, ok := err.(*httpx.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
