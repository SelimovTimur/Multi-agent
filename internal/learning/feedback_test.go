package learning

import (
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

func TestFeedbackAcceptsPartialAnswers(t *testing.T) {
	for _, f := range []AttemptFeedback{
		{Lesson: RatingGood},
		{Avatar: RatingBad},
		{LessonWhy: "скучно"},
		{Unclear: "  слова в тексте  "},
		{Lesson: RatingBad, Avatar: RatingOK, LessonWhy: "долго", Unclear: "грамматика"},
	} {
		if _, err := f.validate(); err != nil {
			t.Errorf("%+v: отказ %v", f, err)
		}
	}
}

func TestFeedbackTrimsTexts(t *testing.T) {
	got, err := AttemptFeedback{Unclear: "  слова \n", AvatarWhy: " тихо "}.validate()
	if err != nil || got.Unclear != "слова" || got.AvatarWhy != "тихо" {
		t.Errorf("получили %+v, %v", got, err)
	}
}

func TestFeedbackRejectsEmptyAndUnknown(t *testing.T) {
	if _, err := (AttemptFeedback{Unclear: "   "}).validate(); err == nil {
		t.Error("пустой опрос принят: в статистике появилась бы строка без ответов")
	}
	long := strings.Repeat("ә", feedbackTextLimit+1)
	cases := map[string]AttemptFeedback{
		"lesson":     {Lesson: "super"},
		"avatar":     {Avatar: "1"},
		"lesson_why": {LessonWhy: long},
		"avatar_why": {AvatarWhy: long},
		"unclear":    {Unclear: long},
	}
	for field, f := range cases {
		_, err := f.validate()
		if httpx.AsError(err).Fields[field] == "" {
			t.Errorf("поле %s: нет отказа по полю, ошибка %v", field, err)
		}
	}
}
