package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/model"
	"github.com/google/uuid"
)

type recognizeCall struct {
	language    string
	interactive bool
}

// fakeASR — распознавание без сервиса: отвечает заданным ответом и
// запоминает, с каким языком и в каком режиме его позвали.
type fakeASR struct {
	mu    sync.Mutex
	calls []recognizeCall
	resp  model.ASRResponse
}

func (f *fakeASR) record(language string, interactive bool) (model.ASRResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recognizeCall{language: language, interactive: interactive})
	return f.resp, nil
}

func (f *fakeASR) Recognize(_ context.Context, _, language string) (model.ASRResponse, error) {
	return f.record(language, false)
}

func (f *fakeASR) RecognizeInteractive(_ context.Context, _, language string) (model.ASRResponse, error) {
	return f.record(language, true)
}

// copyConverter вместо ffmpeg: запись уже «WAV».
type copyConverter struct{}

func (copyConverter) ConvertToWav16k(_ context.Context, in, out string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}

func newTestAPI(t *testing.T, asr Recognizer) *API {
	t.Helper()
	svc := NewService(asr, copyConverter{}, nil, mlgw.New(config.MLConfig{}, nil), t.TempDir())
	return NewAPI(svc, nil)
}

// uploadRequest — форма как у браузера: запись и, если задано, язык урока.
func uploadRequest(t *testing.T, path string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "voice.webm")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("RIFF-test"))
	for name, value := range fields {
		if err := w.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	actor := iam.Actor{Kind: "student", StudentID: uuid.New(), OrgID: uuid.New()}
	return r.WithContext(iam.WithActor(r.Context(), actor))
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("ответ не JSON: %s", rec.Body.String())
	}
	return out
}

func confidence(v float64) *float64 { return &v }

func TestStudentTranscribePassesLanguageAndReviewFields(t *testing.T) {
	asr := &fakeASR{resp: model.ASRResponse{
		Text: "Мин Казанда яшим", Language: "english", Confidence: confidence(0.62),
		NeedsReview: true, ReviewReasons: []string{"low_confidence"},
	}}
	api := newTestAPI(t, asr)

	rec := httptest.NewRecorder()
	api.transcribeStudent(rec, uploadRequest(t, "/my/transcribe", map[string]string{"language": "english"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(asr.calls) != 1 || asr.calls[0] != (recognizeCall{language: "english", interactive: true}) {
		t.Fatalf("вызовы ASR = %+v", asr.calls)
	}
	got := decode(t, rec)
	if got["text"] != "Мин Казанда яшим" || got["language"] != "english" {
		t.Fatalf("ответ = %v", got)
	}
	if got["confidence"] != 0.62 || got["needs_review"] != true {
		t.Fatalf("проверка = %v", got)
	}
	reasons, _ := got["review_reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != "low_confidence" {
		t.Fatalf("review_reasons = %v", got["review_reasons"])
	}
}

func TestTranscribeRejectsMalformedLanguage(t *testing.T) {
	for _, language := range []string{"Tatar", "t", "tatar1", "tat ar", "татар", "en-US", strings.Repeat("a", 17)} {
		t.Run(language, func(t *testing.T) {
			asr := &fakeASR{resp: model.ASRResponse{Text: "Сәлам", Confidence: confidence(0.9)}}
			api := newTestAPI(t, asr)

			rec := httptest.NewRecorder()
			api.transcribeStudent(rec, uploadRequest(t, "/my/transcribe", map[string]string{"language": language}))

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if len(asr.calls) != 0 {
				t.Fatalf("ASR вызван с неверным языком: %+v", asr.calls)
			}
			envelope := decode(t, rec)["error"].(map[string]any)
			fields, _ := envelope["fields"].(map[string]any)
			if envelope["code"] != "validation_failed" || fields["language"] == nil {
				t.Fatalf("ошибка = %v", envelope)
			}
		})
	}
}

func TestOldASRResponseMeansReview(t *testing.T) {
	// asr_service старой версии не присылает ни уверенности, ни решения.
	asr := &fakeASR{resp: model.ASRResponse{Text: "Мин Казанда яшим", Language: "tatar"}}
	api := newTestAPI(t, asr)

	rec := httptest.NewRecorder()
	api.transcribeStudent(rec, uploadRequest(t, "/my/transcribe", map[string]string{"language": "tatar"}))

	got := decode(t, rec)
	if got["confidence"] != nil || got["needs_review"] != true {
		t.Fatalf("ответ = %v", got)
	}
	reasons, _ := got["review_reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != "no_confidence" {
		t.Fatalf("review_reasons = %v", got["review_reasons"])
	}
}

func TestLocalModelReviewIsPassedThrough(t *testing.T) {
	asr := &fakeASR{resp: model.ASRResponse{
		Text: "Кызым", Language: "tatar", NeedsReview: true,
		ReviewReasons: []string{"no_confidence", "foreign_letters", "quiet_audio"},
	}}
	api := newTestAPI(t, asr)

	rec := httptest.NewRecorder()
	api.transcribeStudent(rec, uploadRequest(t, "/my/transcribe", nil))

	got := decode(t, rec)
	if got["confidence"] != nil || got["needs_review"] != true {
		t.Fatalf("ответ = %v", got)
	}
	reasons, _ := got["review_reasons"].([]any)
	if len(reasons) != 3 || reasons[1] != "foreign_letters" || reasons[2] != "quiet_audio" {
		t.Fatalf("review_reasons = %v", got["review_reasons"])
	}
}
