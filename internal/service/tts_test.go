package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
)

type ttsRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn ttsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func audioHTTPClient(t *testing.T, audio []byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: ttsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/synthesize" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), "Исәнме") {
			t.Fatalf("body = %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"audio/wav"}},
			Body:       io.NopCloser(bytes.NewReader(audio)),
			Request:    req,
		}, nil
	})}
}

func TestExtractAvatarSpeech(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "phrases with translations",
			input: "Исәнме! (Здравствуй!) Текстта нинди мәсьәләләр бар? (Какие вопросы есть в тексте?)",
			want:  "Исәнме!\nТекстта нинди мәсьәләләр бар?",
		},
		{
			name:  "normalizes whitespace",
			input: "  Беренче җөмлә.  \n (Первое предложение.) \n Икенче җөмлә. ",
			want:  "Беренче җөмлә.\nИкенче җөмлә.",
		},
		{
			name:  "nested parentheses",
			input: "Рәхмәт! (Спасибо (большое)!) Сау бул! (До свидания!)",
			want:  "Рәхмәт!\nСау бул!",
		},
		{name: "translation only", input: "(Только перевод)", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractAvatarSpeech(tt.input); got != tt.want {
				t.Fatalf("ExtractAvatarSpeech() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTTSServiceSynthesize(t *testing.T) {
	wav := []byte("RIFF-test-wav")
	svc := NewTTSService(&config.Config{TTSServiceURL: "http://tts.test", MaxConcurrentTTS: 1})
	svc.client = audioHTTPClient(t, wav)
	got, err := svc.Synthesize(context.Background(), "Исәнме!", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(wav) {
		t.Fatalf("audio = %q", got)
	}
}

func TestTTSServiceAvatarJob(t *testing.T) {
	svc := NewTTSService(&config.Config{TTSServiceURL: "http://tts.test", MaxConcurrentTTS: 1})
	svc.client = audioHTTPClient(t, []byte("RIFF-ready"))
	if id := svc.StartAvatarSynthesis("session", "mari", "Текст"); id != "" {
		t.Fatalf("non-tatar job id = %q", id)
	}

	id := svc.StartAvatarSynthesis("session", "tatar", "Исәнме! (Привет!)")
	if id == "" {
		t.Fatal("tatar job id is empty")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, ok := svc.Job("session", id)
		if !ok {
			t.Fatal("job not found")
		}
		if job.Status == TTSJobReady {
			if string(job.Audio) != "RIFF-ready" {
				t.Fatalf("audio = %q", job.Audio)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not become ready")
}
