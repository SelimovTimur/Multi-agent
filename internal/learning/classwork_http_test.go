package learning

// Ответы ручек — договор с двумя экранами сразу, учителя и ученика. Здесь
// проверяется не арифметика (она в остальных тестах), а форма: имена полей,
// коды ответов и тексты отказов из docs/backend/class-lessons-api.md.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// contractRouter — ручки модуля без проверки прав: права проверяет Guard, а
// здесь проверяется то, что ручки отвечают. Актор приходит из теста.
func contractRouter(api *API, actor *iam.Actor) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(iam.WithActor(req.Context(), *actor)))
		})
	})
	r.Post("/assignments", api.assign)
	r.Get("/classes/{classID}/assignments", api.classAssignments)
	r.Get("/classes/{classID}/students/{studentID}/card", api.studentCard)
	r.Get("/assignments/{assignmentID}/live", api.live)
	r.Post("/assignments/{assignmentID}/issues", api.issue)
	r.Get("/assignments/{assignmentID}/students/{studentID}", api.studentPanel)
	r.Post("/issues/{issueID}/finish", api.finishIssue)
	r.Post("/issues/{issueID}/extend", api.extendIssue)
	r.Delete("/issues/{issueID}", api.cancelIssue)
	r.Get("/my/lessons", api.myLessons)
	r.Post("/my/lessons/{assignmentID}/start", api.start)
	r.Get("/my/attempts/{attemptID}/tasks", api.sectionTasks)
	r.Post("/my/attempts/{attemptID}/tasks/{taskKey}/open", api.openTask)
	r.Post("/my/attempts/{attemptID}/tasks/{taskKey}/done", api.taskDone)
	r.Post("/my/attempts/{attemptID}/answer", api.answer)
	return r
}

type jsonObj = map[string]any

func call(t *testing.T, h http.Handler, method, path string, body any) (int, jsonObj) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
	var out jsonObj
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: ответ не JSON: %s", method, path, rec.Body.String())
	}
	return rec.Code, out
}

// hasKeys — у объекта ровно те поля, что в контракте (лишние не запрещены,
// но перечисленные обязаны быть).
func hasKeys(t *testing.T, what string, obj any, keys ...string) jsonObj {
	t.Helper()
	m, ok := obj.(jsonObj)
	if !ok {
		t.Fatalf("%s: не объект: %#v", what, obj)
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: нет поля %q в %v", what, k, m)
		}
	}
	return m
}

func first(t *testing.T, what string, v any) jsonObj {
	t.Helper()
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("%s: пустой список: %#v", what, v)
	}
	return list[0].(jsonObj)
}

func refusal(t *testing.T, what string, out jsonObj, code, msg, field string) {
	t.Helper()
	e := hasKeys(t, what, out["error"], "code", "message")
	if e["code"] != code {
		t.Errorf("%s: код %v, ожидали %s", what, e["code"], code)
	}
	got := e["message"]
	if field != "" {
		got = hasKeys(t, what, e["fields"], field)[field]
	}
	if got != msg {
		t.Errorf("%s: %q, ожидали %q", what, got, msg)
	}
}

func TestClassworkHTTPContract(t *testing.T) {
	f := newFixture(t)
	actor := f.teacher
	h := contractRouter(&API{svc: f.svc}, &actor)

	// Назначить: сроки игнорируются, ответ — как раньше плюс строка урока.
	code, out := call(t, h, http.MethodPost, "/assignments", jsonObj{
		"lesson_version_id": f.versionID, "class_id": f.classID,
		"opens_at": "не дата", "closes_at": nil,
	})
	if code != http.StatusCreated {
		t.Fatalf("назначить: %d %v", code, out)
	}
	asg := hasKeys(t, "assignment", out["assignment"], "id", "class_id", "lesson_version_id", "lesson_id",
		"title", "level", "version", "state", "created_at",
		"tasks_total", "tasks_issued", "class_open", "home", "legacy")
	if asg["tasks_total"] != 4.0 || asg["tasks_issued"] != 0.0 || asg["home"] != nil || asg["class_open"] != false {
		t.Errorf("новое назначение — «выдано 0 из 4»: %v", asg)
	}
	assignmentID := asg["id"].(string)
	code, out = call(t, h, http.MethodPost, "/assignments", jsonObj{
		"lesson_version_id": f.versionID, "class_id": f.classID,
	})
	if code != http.StatusConflict {
		t.Fatalf("второе назначение: %d", code)
	}
	refusal(t, "второе назначение", out, "conflict", "Этот урок уже назначен классу", "")

	// Выдать.
	issuePath := "/assignments/" + assignmentID + "/issues"
	code, out = call(t, h, http.MethodPost, issuePath, jsonObj{"kind": "class", "task_keys": []string{}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("пустая выдача: %d", code)
	}
	refusal(t, "пустая выдача", out, "validation_failed", "Отметьте хотя бы одно задание", "task_keys")
	code, out = call(t, h, http.MethodPost, issuePath, jsonObj{"kind": "home", "task_keys": []string{"text"}})
	refusal(t, "домашка без срока", out, "validation_failed", "Укажите срок", "due_date")

	code, out = call(t, h, http.MethodPost, issuePath, jsonObj{
		"kind": "class", "task_keys": []string{ExerciseTaskKey(f.match), "text"},
	})
	if code != http.StatusCreated {
		t.Fatalf("выдать на уроке: %d %v", code, out)
	}
	is := hasKeys(t, "issue", out["issue"], "id", "kind", "task_keys", "opened_at", "due_at", "closed_at",
		"open", "started", "done_students", "legacy")
	if is["open"] != true || is["closed_at"] != nil || is["legacy"] != false ||
		!strings.HasSuffix(is["due_at"].(string), "T23:59:59+03:00") {
		t.Errorf("выдача на уроке до конца дня по времени школы: %v", is)
	}
	if keys := is["task_keys"].([]any); len(keys) != 2 || keys[0] != "text" {
		t.Errorf("task_keys в порядке урока: %v", keys)
	}
	classIssue := is["id"].(string)

	code, out = call(t, h, http.MethodPost, issuePath, jsonObj{
		"kind": "home", "task_keys": []string{"dialog"}, "due_date": dateIn(2),
	})
	if code != http.StatusCreated {
		t.Fatalf("задать на дом: %d %v", code, out)
	}
	homeIssue := out["issue"].(jsonObj)["id"].(string)
	code, out = call(t, h, http.MethodPost, issuePath, jsonObj{
		"kind": "home", "task_keys": []string{ExerciseTaskKey(f.custom)}, "due_date": dateIn(2),
	})
	if code != http.StatusConflict {
		t.Fatalf("вторая домашка: %d", code)
	}
	refusal(t, "вторая домашка", out, "conflict", "Домашняя работа по уроку уже задана", "")

	// Ученик.
	actor = f.students[0]
	code, out = call(t, h, http.MethodGet, "/my/lessons", nil)
	if code != http.StatusOK {
		t.Fatalf("мои занятия: %d %v", code, out)
	}
	lesson := hasKeys(t, "lesson", first(t, "lessons", out["lessons"]), "assignment_id", "lesson_id", "title",
		"level", "class", "attempt_id", "tasks_total", "tasks_done", "class_section", "home_section", "finished")
	hasKeys(t, "class", lesson["class"], "id", "name")
	hasKeys(t, "class_section", lesson["class_section"], "tasks", "left", "begun")
	hasKeys(t, "home_section", lesson["home_section"], "tasks", "left", "begun", "due_at")
	if lesson["attempt_id"] != nil || lesson["title"] != "Гаилә — Семья" {
		t.Errorf("урок ещё не начат: %v", lesson)
	}

	code, out = call(t, h, http.MethodPost, "/my/lessons/"+assignmentID+"/start", jsonObj{})
	if code != http.StatusOK {
		t.Fatalf("начать: %d %v", code, out)
	}
	attemptID := out["attempt"].(jsonObj)["id"].(string)

	code, out = call(t, h, http.MethodGet, "/my/attempts/"+attemptID+"/tasks?section=class", nil)
	if code != http.StatusOK {
		t.Fatalf("задания раздела: %d %v", code, out)
	}
	hasKeys(t, "tasks", out, "section", "open", "closed_reason", "due_at", "tasks")
	task := hasKeys(t, "task", first(t, "tasks", out["tasks"]), "key", "n", "kind", "type", "title",
		"questions", "answered", "done")
	if out["section"] != "class" || out["open"] != true || out["closed_reason"] != nil || task["key"] != "text" {
		t.Errorf("раздел на уроке: %v", out)
	}

	exPath := "/my/attempts/" + attemptID + "/tasks/" + url.PathEscape(ExerciseTaskKey(f.match))
	exPath = strings.Replace(exPath, ":", "%3A", 1)
	if code, out = call(t, h, http.MethodPost, exPath+"/open", jsonObj{}); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("открыть упражнение (двоеточие закодировано): %d %v", code, out)
	}
	code, out = call(t, h, http.MethodPost, exPath+"/done", jsonObj{})
	if code != http.StatusConflict {
		t.Fatalf("упражнение кнопкой «Дальше»: %d", code)
	}
	refusal(t, "упражнение кнопкой", out, "conflict", "Задание засчитывается по ответам", "")
	code, out = call(t, h, http.MethodPost, "/my/attempts/"+attemptID+"/answer", jsonObj{
		"exercise_id": f.match, "item_id": "әни", "answer": "мама",
	})
	if code != http.StatusOK || out["correct"] != true {
		t.Fatalf("ответ: %d %v", code, out)
	}
	if code, _ = call(t, h, http.MethodPost, "/my/attempts/"+attemptID+"/tasks/text/done", jsonObj{}); code != http.StatusOK {
		t.Fatalf("текст прочитан: %d", code)
	}
	code, out = call(t, h, http.MethodPost, "/my/attempts/"+attemptID+"/answer", jsonObj{
		"exercise_id": f.custom, "item_id": "x", "answer": "y",
	})
	if code != http.StatusConflict {
		t.Fatalf("ответ на невыданное: %d %v", code, out)
	}
	refusal(t, "ответ на невыданное", out, "conflict", "Это задание ещё не выдано", "")

	// Учитель: таблица, панель, карточка, вкладка «Уроки».
	actor = f.teacher
	code, out = call(t, h, http.MethodGet, "/assignments/"+assignmentID+"/live", nil)
	if code != http.StatusOK {
		t.Fatalf("урок в классе: %d %v", code, out)
	}
	hasKeys(t, "live", out, "assignment", "class", "server_time", "tasks", "issues", "has_home", "students")
	hasKeys(t, "live.assignment", out["assignment"], "id", "class_id", "lesson_id", "lesson_version_id",
		"title", "level", "version")
	hasKeys(t, "live.class", out["class"], "id", "name")
	hasKeys(t, "live.task", first(t, "live.tasks", out["tasks"]), "key", "n", "kind", "type", "type_label",
		"title", "questions", "issue_id", "done_students")
	hasKeys(t, "live.issue", first(t, "live.issues", out["issues"]), "id", "kind", "task_keys", "opened_at",
		"due_at", "closed_at", "open", "started", "done_students", "legacy")
	student := hasKeys(t, "live.student", first(t, "live.students", out["students"]), "id", "name", "tag",
		"online", "last_seen_at", "cells")
	cell := hasKeys(t, "cell", student["cells"].(jsonObj)[ExerciseTaskKey(f.match)], "state", "answered", "correct", "total")
	if cell["state"] != "doing" || cell["answered"] != 1.0 || cell["total"] != 2.0 || out["has_home"] != true {
		t.Errorf("клетка «1/2»: %v", cell)
	}
	if student["tag"] != "KZ4F" || student["name"] != "Айдар" {
		t.Errorf("тег без решётки, как в контракте: %v", student["tag"])
	}
	if _, ok := student["cells"].(jsonObj)["objectives"]; ok {
		t.Error("клетки только по выданным заданиям")
	}

	code, out = call(t, h, http.MethodGet, "/assignments/"+assignmentID+"/students/"+f.students[0].StudentID.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("панель ученика: %d %v", code, out)
	}
	if ps := hasKeys(t, "panel.student", out["student"], "id", "name", "tag", "online", "last_seen_at"); ps["tag"] != "KZ4F" {
		t.Errorf("тег в панели без решётки: %v", ps["tag"])
	}
	panelTasks := out["tasks"].([]any)
	if len(panelTasks) != 3 {
		t.Fatalf("в панели выданные задания: %d", len(panelTasks))
	}
	text := hasKeys(t, "panel.task", panelTasks[0], "key", "n", "kind", "type", "title", "issue_id", "state",
		"answered", "correct", "total", "minutes", "questions")
	if text["state"] != "done" || text["minutes"] == nil {
		t.Errorf("текст сделан, минуты есть: %v", text)
	}
	ex := panelTasks[1].(jsonObj)
	q := hasKeys(t, "panel.question", first(t, "questions", ex["questions"]), "prompt", "answer", "correct", "expected")
	if q["prompt"] != "әни" || q["answer"] != "мама" || q["expected"] != "мама" || ex["minutes"] != nil {
		t.Errorf("отвеченный вопрос словами: %v (минут у несделанного нет: %v)", q, ex["minutes"])
	}

	code, out = call(t, h, http.MethodGet, "/classes/"+f.classID.String()+"/students/"+
		f.students[0].StudentID.String()+"/card", nil)
	if code != http.StatusOK {
		t.Fatalf("карточка: %d %v", code, out)
	}
	hasKeys(t, "summary", out["summary"], "assigned", "passed", "correct_answers", "total_questions",
		"correct_percent", "last_seen_at")
	row := hasKeys(t, "lessons", first(t, "lessons", out["lessons"]), "assignment_id", "title", "level",
		"assigned_at", "state", "tasks_total", "tasks_done", "issues", "started", "completed",
		"correct_answers", "total_questions", "correct_percent", "last_seen_at", "conversation")
	if row["started"] != true {
		t.Errorf("урок открыт — в строке есть попытка: %v", row)
	}
	ci := hasKeys(t, "lessons.issue", first(t, "lessons.issues", row["issues"]), "id", "kind", "opened_at",
		"due_at", "closed_at", "open", "legacy", "tasks")
	ct := hasKeys(t, "lessons.task", first(t, "lessons.tasks", ci["tasks"]), "key", "n", "state", "correct", "total")
	if ct["key"] != "text" || ct["state"] != "done" || ct["correct"] != nil || ct["total"] != nil {
		t.Errorf("задание без вопросов — «✓» без дроби: %v", ct)
	}

	code, out = call(t, h, http.MethodGet, "/classes/"+f.classID.String()+"/assignments", nil)
	if code != http.StatusOK {
		t.Fatalf("вкладка «Уроки»: %d", code)
	}
	tab := first(t, "assignments", out["assignments"])
	home := hasKeys(t, "home", tab["home"], "due_at", "open")
	if tab["tasks_issued"] != 3.0 || tab["class_open"] != true || home["open"] != true {
		t.Errorf("строка урока: %v", tab)
	}

	// Завершить, продлить, отменить.
	code, out = call(t, h, http.MethodPost, "/issues/"+homeIssue+"/finish", jsonObj{})
	refusal(t, "завершить домашку", out, "conflict", "Завершить можно только работу на уроке", "")
	code, out = call(t, h, http.MethodPost, "/issues/"+classIssue+"/finish", jsonObj{})
	if code != http.StatusOK || out["issue"].(jsonObj)["closed_at"] == nil {
		t.Fatalf("завершить работу на уроке: %d %v", code, out)
	}
	code, out = call(t, h, http.MethodPost, "/issues/"+classIssue+"/finish", jsonObj{})
	refusal(t, "завершить дважды", out, "conflict", "Работа на уроке уже завершена", "")
	code, out = call(t, h, http.MethodPost, "/issues/"+classIssue+"/extend", jsonObj{"due_date": dateIn(5)})
	refusal(t, "продлить урок", out, "conflict", "Продлить можно только домашнюю работу", "")
	code, out = call(t, h, http.MethodPost, "/issues/"+homeIssue+"/extend", jsonObj{"due_date": dateIn(1)})
	refusal(t, "продлить назад", out, "validation_failed", "Новый срок должен быть позже текущего", "due_date")
	code, out = call(t, h, http.MethodPost, "/issues/"+homeIssue+"/extend", jsonObj{"due_date": dateIn(5)})
	if code != http.StatusOK || !strings.Contains(out["issue"].(jsonObj)["due_at"].(string), dateIn(5)) {
		t.Fatalf("продлить домашку: %d %v", code, out)
	}
	code, out = call(t, h, http.MethodDelete, "/issues/"+classIssue, nil)
	refusal(t, "отменить начатое", out, "conflict", "Задания этой выдачи уже начали — отменить нельзя", "")
	if code, out = call(t, h, http.MethodDelete, "/issues/"+homeIssue, nil); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("отменить нетронутую домашку: %d %v", code, out)
	}
	if code, _ = call(t, h, http.MethodDelete, "/issues/"+uuid.NewString(), nil); code != http.StatusNotFound {
		t.Errorf("чужая выдача — 404: %d", code)
	}

	// Ученик после завершения работы на уроке.
	actor = f.students[0]
	code, out = call(t, h, http.MethodGet, "/my/attempts/"+attemptID+"/tasks?section=class", nil)
	if code != http.StatusOK || out["open"] != false || out["closed_reason"] != "class_finished" || len(out["tasks"].([]any)) != 0 {
		t.Errorf("учитель завершил работу на уроке: %d %v", code, out)
	}
	code, out = call(t, h, http.MethodPost, "/my/attempts/"+attemptID+"/answer", jsonObj{
		"exercise_id": f.match, "item_id": "әти", "answer": "папа",
	})
	refusal(t, "ответ после завершения", out, "conflict", "Учитель завершил работу на уроке", "")
	code, out = call(t, h, http.MethodGet, "/my/attempts/"+attemptID+"/tasks", nil)
	if code != http.StatusOK || out["section"] != nil {
		t.Errorf("без раздела — section null: %v", out)
	}
}
