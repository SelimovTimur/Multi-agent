package speech

import (
	"fmt"
	"net/http"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// API — голосовой ввод и синтез.
type API struct {
	svc   *Service
	guard *iam.Guard
}

func NewAPI(svc *Service, guard *iam.Guard) *API {
	return &API{svc: svc, guard: guard}
}

const maxStudentUpload = 20 << 20

func (a *API) Mount(r chi.Router) {
	g := a.guard
	// Диктовка ученика в разговоре с аватаром. Прав из матрицы у него нет,
	// поэтому PermAuthenticated: принадлежность урока проверяет learning.
	g.Handle(r, http.MethodPost, "/my/transcribe", iam.PermAuthenticated, a.transcribeStudent)
	g.Handle(r, http.MethodPost, "/my/tts", iam.PermAuthenticated, a.speak)
}

// transcribeStudent — диктовка в диалоге: человек ждёт результат сейчас.
func (a *API) transcribeStudent(w http.ResponseWriter, r *http.Request) {
	// Лимит на теле запроса, а не только на форме: без него отправитель
	// может занять диск, пока мы разбираем multipart.
	limit := int64(maxStudentUpload)
	// Служебные поля multipart не входят в заявленный размер файла.
	r.Body = http.MaxBytesReader(w, r.Body, limit+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		httpx.Fail(w, r, httpx.Validation("Не удалось прочитать файл",
			map[string]string{"file": "Файл слишком большой или повреждён"}))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("file")
	if err != nil {
		httpx.Fail(w, r, httpx.FieldError("file", "Прикрепите запись"))
		return
	}
	defer file.Close()
	if header.Size > limit {
		httpx.Fail(w, r, httpx.FieldError("file", fmt.Sprintf("Файл больше %d МБ", limit>>20)))
		return
	}

	// Язык урока — необязательное поле формы: по нему ASR выбирает подсказку
	// (татарская орфография или короткая подсказка другого языка). Формат
	// проверяет Transcribe — до перекодирования и очереди на ASR.
	language := r.PostFormValue("language")

	actor := iam.MustActor(r.Context())
	out, err := a.svc.Transcribe(r.Context(), actor.OrgID, actorID(actor), file, header.Filename,
		language, true)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// speak озвучивает текст — реплику аватара.
//
// Отдаёт WAV потоком, а не ссылкой: файл живёт одну реплику, и заводить
// ради него хранилище незачем.
func (a *API) speak(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
		// AttemptID — из какой попытки реплика. По ней берётся тон разговора,
		// заданный учителем (NR-29): сам тон клиент не присылает, иначе им
		// можно было бы управлять синтезом в обход настройки урока.
		AttemptID string `json:"attempt_id"`
	}
	if err := httpx.DecodeJSON(r, &in, 16*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	actor := iam.MustActor(r.Context())
	attemptID, _ := uuid.Parse(in.AttemptID)
	style := a.svc.StyleForAttempt(r.Context(), actor, attemptID)
	audio, err := a.svc.Synthesize(r.Context(), actor.OrgID, actorID(actor), in.Text, style)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio)
}

// actorID — чей это вызов. У ученика UserID пустой, и в учёт идёт его
// идентификатор: иначе строки в ops.pipeline_calls были бы ничьи.
func actorID(a iam.Actor) uuid.UUID {
	if a.IsStudent() {
		return a.StudentID
	}
	return a.UserID
}
