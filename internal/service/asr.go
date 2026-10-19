package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/model"
)

// ASRService — клиент для Python-микросервиса распознавания татарской речи.
//
// В Python-версии распознавание было прямо в коде:
//
//	raw_text = wav_recognition_with_chunk(wav_path)
//
// Здесь модель wav2vec2 живёт в отдельном Python-сервере,
// а Go отправляет ему файл по HTTP и получает текст.
type ASRService struct {
	cfg    *config.Config
	client *http.Client
	sem    chan struct{} // семафор — распознавание тяжёлое, ограничиваем параллельность
}

func NewASRService(cfg *config.Config) *ASRService {
	return &ASRService{
		cfg: cfg,
		client: &http.Client{
			Timeout: 20 * time.Minute, // wav2vec2 на CPU может занять до 15 мин для длинных файлов
		},
		sem: make(chan struct{}, cfg.MaxConcurrentASR),
	}
}

// Recognize отправляет WAV-файл в ASR-сервис и возвращает распознанный текст с языком.
//
// language — язык урока («tatar», «english», …): по нему ASR выбирает
// подсказку модели. Пустая строка — язык из настройки ASR (ASR_API_LANGUAGE).
//
// Работает так:
// 1. Захватываем слот в семафоре (чтобы не перегрузить GPU/CPU)
// 2. Открываем файл, упаковываем в multipart/form-data (как загрузка файла в браузере)
// 3. Отправляем POST на http://asr-service:8000/recognize
// 4. Получаем JSON: {"text", "language", "confidence", "needs_review", "review_reasons"}
func (s *ASRService) Recognize(ctx context.Context, wavPath, language string) (model.ASRResponse, error) {
	return s.recognize(ctx, wavPath, language, purposeMaterial, true)
}

// RecognizeInteractive keeps the caller context attached so an abandoned chat
// dictation request does not continue occupying ASR after the browser leaves.
func (s *ASRService) RecognizeInteractive(ctx context.Context, wavPath, language string) (model.ASRResponse, error) {
	return s.recognize(ctx, wavPath, language, purposeAnswer, false)
}

// RecognizeMaterial — материал учителя, который обрывается вместе с уходом
// клиента (ролик по ссылке): без уверенности, как Recognize, но с контекстом
// вызывающего, как RecognizeInteractive.
func (s *ASRService) RecognizeMaterial(ctx context.Context, wavPath, language string) (model.ASRResponse, error) {
	return s.recognize(ctx, wavPath, language, purposeMaterial, false)
}

// Что распознаём: ответ ученика (с уверенностью, asr_service purpose=answer)
// или материал учителя (без неё — с ней долгая запись распознаётся вдвое
// дольше, и провайдер её не дослушивал).
const (
	purposeAnswer   = "answer"
	purposeMaterial = "material"
)

func (s *ASRService) recognize(ctx context.Context, wavPath, language, purpose string, detachAfterStart bool) (model.ASRResponse, error) {
	// Захватываем слот — если все заняты, ждём
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return model.ASRResponse{}, ctx.Err()
	}

	// Открываем WAV-файл
	f, err := os.Open(wavPath)
	if err != nil {
		return model.ASRResponse{}, fmt.Errorf("не удалось открыть wav файл: %w", err)
	}
	defer f.Close()

	// Формируем multipart запрос (как <input type="file"> в HTML-форме)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка создания формы: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка копирования файла в форму: %w", err)
	}
	// Без поля ASR берёт язык из своей настройки — так работают вызовы,
	// которым язык урока неизвестен.
	if language != "" {
		if err := writer.WriteField("language", language); err != nil {
			return model.ASRResponse{}, fmt.Errorf("ошибка создания формы: %w", err)
		}
	}
	if err := writer.WriteField("purpose", purpose); err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка создания формы: %w", err)
	}
	writer.Close()

	// Длинный lesson-pipeline не прерываем из-за отключившегося браузера,
	// но короткая диктовка чата сохраняет caller context и может быть отменена.
	requestContext := ctx
	if detachAfterStart {
		requestContext = context.Background()
	}
	asrCtx, cancel := context.WithTimeout(requestContext, 20*time.Minute)
	defer cancel()
	url := s.cfg.ASRServiceURL + "/recognize"
	req, err := http.NewRequestWithContext(asrCtx, http.MethodPost, url, &buf)
	if err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка создания запроса ASR: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.client.Do(req)
	if err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка вызова ASR: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка чтения ответа ASR: %w", err)
	}

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var rejected model.ASRResponse
		if json.Unmarshal(respBody, &rejected) == nil && rejected.Error != "" {
			return rejected, nil
		}
	}
	if resp.StatusCode != http.StatusOK {
		return model.ASRResponse{}, fmt.Errorf("ASR вернул статус %d: %s", resp.StatusCode, string(respBody))
	}

	// Разбираем ответ: {"text": "...", "needs_review": false, ..., "error": "..."}
	var asrResp model.ASRResponse
	if err := json.Unmarshal(respBody, &asrResp); err != nil {
		return model.ASRResponse{}, fmt.Errorf("ошибка разбора ответа ASR: %w", err)
	}

	if asrResp.Error != "" {
		return model.ASRResponse{}, fmt.Errorf("ошибка распознавания: %s", asrResp.Error)
	}

	return asrResp, nil
}
