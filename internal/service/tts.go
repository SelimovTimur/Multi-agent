package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/google/uuid"
)

const (
	TTSJobPending = "pending"
	TTSJobReady   = "ready"
	TTSJobFailed  = "failed"

	ttsJobTTL          = time.Hour
	maxTTSResponseSize = 32 << 20
)

type TTSJobResult struct {
	Status string
	Audio  []byte
}

type ttsJob struct {
	sessionID string
	status    string
	audio     []byte
	createdAt time.Time
}

// TTSService вызывает Python Silero-сервис и хранит короткоживущие результаты
// фонового синтеза, пока браузер не заберёт готовый WAV.
type TTSService struct {
	baseURL string
	client  *http.Client
	sem     chan struct{}

	mu   sync.RWMutex
	jobs map[string]*ttsJob
}

func NewTTSService(cfg *config.Config) *TTSService {
	maxConcurrent := cfg.MaxConcurrentTTS
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &TTSService{
		baseURL: strings.TrimRight(cfg.TTSServiceURL, "/"),
		client:  &http.Client{Timeout: 3 * time.Minute},
		sem:     make(chan struct{}, maxConcurrent),
		jobs:    make(map[string]*ttsJob),
	}
}

// ExtractAvatarSpeech удаляет переводы в круглых скобках и переносит каждую
// оставшуюся фразу на отдельную строку. Вложенные скобки тоже поддерживаются.
func ExtractAvatarSpeech(reply string) string {
	segments := make([]string, 0, 4)
	var current strings.Builder
	depth := 0

	flush := func() {
		text := strings.Join(strings.Fields(current.String()), " ")
		if text != "" {
			segments = append(segments, text)
		}
		current.Reset()
	}

	for _, r := range reply {
		switch r {
		case '(':
			if depth == 0 {
				flush()
			}
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				if unicode.IsSpace(r) {
					current.WriteRune(' ')
				} else {
					current.WriteRune(r)
				}
			}
		}
	}
	flush()

	return strings.Join(segments, "\n")
}

// StartAvatarSynthesis создаёт фоновую задачу только для татарского диалога.
// Пустой ID означает, что аудио для этого ответа не предусмотрено.
func (s *TTSService) StartAvatarSynthesis(sessionID, language, reply string) string {
	if s == nil || strings.ToLower(strings.TrimSpace(language)) != "tatar" {
		return ""
	}
	text := ExtractAvatarSpeech(reply)
	if text == "" {
		return ""
	}

	id := uuid.NewString()
	s.mu.Lock()
	s.cleanupLocked(time.Now())
	s.jobs[id] = &ttsJob{
		sessionID: sessionID,
		status:    TTSJobPending,
		createdAt: time.Now(),
	}
	s.mu.Unlock()

	go s.runJob(id, text)
	return id
}

func (s *TTSService) runJob(id, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Старый контур не знает настройки учителя — озвучиваем нейтрально.
	audio, err := s.Synthesize(ctx, text, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	if err != nil {
		job.status = TTSJobFailed
		log.Printf("Не удалось синтезировать голосовое сообщение %s: %v", id, err)
		return
	}
	job.audio = audio
	job.status = TTSJobReady
}

// Synthesize озвучивает текст. style — тон разговора, заданный учителем
// (NR-29): локальная модель его не умеет и игнорирует, внешний синтез
// принимает подсказкой.
func (s *TTSService) Synthesize(ctx context.Context, text, style string) ([]byte, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	body, err := json.Marshal(map[string]string{"text": text, "style": style})
	if err != nil {
		return nil, fmt.Errorf("не удалось собрать TTS-запрос: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/synthesize", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("не удалось создать TTS-запрос: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ошибка вызова TTS: %w", err)
	}
	defer resp.Body.Close()

	audio, err := io.ReadAll(io.LimitReader(resp.Body, maxTTSResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа TTS: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TTS вернул статус %d: %s", resp.StatusCode, strings.TrimSpace(string(audio)))
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("TTS вернул пустой WAV")
	}
	if len(audio) > maxTTSResponseSize {
		return nil, fmt.Errorf("ответ TTS превышает лимит %d байт", maxTTSResponseSize)
	}
	return audio, nil
}

func (s *TTSService) Job(sessionID, id string) (TTSJobResult, bool) {
	if s == nil || id == "" {
		return TTSJobResult{}, false
	}

	now := time.Now()
	s.mu.Lock()
	s.cleanupLocked(now)
	job, ok := s.jobs[id]
	if !ok || job.sessionID != sessionID {
		s.mu.Unlock()
		return TTSJobResult{}, false
	}
	result := TTSJobResult{Status: job.status, Audio: job.audio}
	s.mu.Unlock()
	return result, true
}

func (s *TTSService) cleanupLocked(now time.Time) {
	for id, job := range s.jobs {
		if now.Sub(job.createdAt) > ttsJobTTL {
			delete(s.jobs, id)
		}
	}
}
