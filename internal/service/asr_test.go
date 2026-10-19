package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/NATIVE/NATIVE/internal/config"
)

// asrStub — asr_service /recognize: запоминает поле language и отвечает
// заданным JSON.
func asrStub(t *testing.T, status int, body string, gotLanguage *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/recognize" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("нет файла в форме: %v", err)
		}
		data, _ := io.ReadAll(file)
		if string(data) != "RIFF-test" {
			t.Errorf("file = %q", data)
		}
		*gotLanguage = append(*gotLanguage, r.MultipartForm.Value["language"]...)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testWav(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "voice.wav")
	if err := os.WriteFile(path, []byte("RIFF-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestASRRecognizeSendsLanguageAndReadsReviewFields(t *testing.T) {
	var languages []string
	srv := asrStub(t, http.StatusOK, `{"text":"Мин Казанда яшим","language":"tatar","confidence":0.62,
		"needs_review":true,"review_reasons":["low_confidence"],"diagnostics":{"attempts":1}}`, &languages)
	svc := NewASRService(&config.Config{ASRServiceURL: srv.URL, MaxConcurrentASR: 1})

	got, err := svc.RecognizeInteractive(context.Background(), testWav(t), "tatar")
	if err != nil {
		t.Fatal(err)
	}
	if len(languages) != 1 || languages[0] != "tatar" {
		t.Fatalf("language в форме = %v", languages)
	}
	if got.Text != "Мин Казанда яшим" || got.Language != "tatar" {
		t.Fatalf("ответ = %+v", got)
	}
	if got.Confidence == nil || *got.Confidence != 0.62 {
		t.Fatalf("confidence = %v", got.Confidence)
	}
	if !got.NeedsReview || len(got.ReviewReasons) != 1 || got.ReviewReasons[0] != "low_confidence" {
		t.Fatalf("проверка = %v %v", got.NeedsReview, got.ReviewReasons)
	}
}

func TestASRRecognizeWithoutLanguageLeavesFieldOut(t *testing.T) {
	var languages []string
	srv := asrStub(t, http.StatusOK, `{"text":"Сәлам","language":"tatar"}`, &languages)
	svc := NewASRService(&config.Config{ASRServiceURL: srv.URL, MaxConcurrentASR: 1})

	got, err := svc.Recognize(context.Background(), testWav(t), "")
	if err != nil {
		t.Fatal(err)
	}
	// Пустой язык — ASR берёт свой ASR_API_LANGUAGE, поле не отправляется.
	if len(languages) != 0 {
		t.Fatalf("language в форме = %v", languages)
	}
	// Ответ старого asr_service: полей проверки нет, ничего не придумываем.
	if got.Confidence != nil || got.NeedsReview || got.ReviewReasons != nil {
		t.Fatalf("ответ = %+v", got)
	}
}

func TestASRRecognizeKeepsUnclearAudioRejection(t *testing.T) {
	var languages []string
	srv := asrStub(t, http.StatusUnprocessableEntity,
		`{"text":"","error":"В записи тишина: не удалось разобрать речь. Замените запись."}`, &languages)
	svc := NewASRService(&config.Config{ASRServiceURL: srv.URL, MaxConcurrentASR: 1})

	got, err := svc.RecognizeInteractive(context.Background(), testWav(t), "english")
	if err != nil {
		t.Fatal(err)
	}
	if got.Error == "" || got.Text != "" {
		t.Fatalf("ответ = %+v", got)
	}
}

// Ответ ученика и материал учителя распознаются по-разному: ответу нужна
// уверенность, материалу — нет (с ней долгая запись не дослушивалась).
func TestASRRecognizeTellsAnswerFromMaterial(t *testing.T) {
	var purposes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		purposes = append(purposes, r.MultipartForm.Value["purpose"]...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"Сәлам","language":"tatar","needs_review":false,"review_reasons":[]}`)
	}))
	t.Cleanup(srv.Close)
	asr := NewASRService(&config.Config{ASRServiceURL: srv.URL, MaxConcurrentASR: 1})

	if _, err := asr.RecognizeInteractive(context.Background(), testWav(t), "tatar"); err != nil {
		t.Fatal(err)
	}
	if _, err := asr.Recognize(context.Background(), testWav(t), "tatar"); err != nil {
		t.Fatal(err)
	}
	if len(purposes) != 2 || purposes[0] != "answer" || purposes[1] != "material" {
		t.Fatalf("purpose в форме = %v, ожидали [answer material]", purposes)
	}
}

// Ролик по ссылке обрывается вместе с уходом учителя, но распознаётся как
// материал — без уверенности, иначе долгая запись не дослушивалась.
func TestASRRecognizeMaterialIsAttachedButMaterial(t *testing.T) {
	var purposes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		purposes = append(purposes, r.MultipartForm.Value["purpose"]...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"Сәлам","language":"tatar","needs_review":false,"review_reasons":[]}`)
	}))
	t.Cleanup(srv.Close)
	asr := NewASRService(&config.Config{ASRServiceURL: srv.URL, MaxConcurrentASR: 1})
	if _, err := asr.RecognizeMaterial(context.Background(), testWav(t), "tatar"); err != nil {
		t.Fatal(err)
	}
	if len(purposes) != 1 || purposes[0] != "material" {
		t.Fatalf("purpose = %v", purposes)
	}
}
