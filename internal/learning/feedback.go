package learning

import (
	"context"
	"fmt"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Оценки в опросе ученика — три смайлика.
const (
	RatingBad  = "bad"  // 🙁
	RatingOK   = "ok"   // 😐
	RatingGood = "good" // 🙂
)

// feedbackTextLimit — предел каждого свободного ответа. Ученик пишет с
// телефона пару фраз, а не сочинение.
const feedbackTextLimit = 1000

// AttemptFeedback — ответы ученика на опрос после урока.
//
// Любое поле можно пропустить: пустое значит «не отвечал», а не «плохо».
type AttemptFeedback struct {
	Lesson    string // Rating*, "" — без ответа
	Avatar    string // Rating*, "" — без ответа
	LessonWhy string // почему урок не понравился
	AvatarWhy string // почему не понравился разговор с аватаром
	Unclear   string // что было непонятно или сложно
}

func validRating(r string) bool {
	return r == "" || r == RatingBad || r == RatingOK || r == RatingGood
}

func (f AttemptFeedback) validate() (AttemptFeedback, error) {
	f.LessonWhy = strings.TrimSpace(f.LessonWhy)
	f.AvatarWhy = strings.TrimSpace(f.AvatarWhy)
	f.Unclear = strings.TrimSpace(f.Unclear)
	fields := map[string]string{}
	if !validRating(f.Lesson) {
		fields["lesson"] = "Выбери один из смайликов"
	}
	if !validRating(f.Avatar) {
		fields["avatar"] = "Выбери один из смайликов"
	}
	for name, text := range map[string]string{
		"lesson_why": f.LessonWhy, "avatar_why": f.AvatarWhy, "unclear": f.Unclear,
	} {
		if len([]rune(text)) > feedbackTextLimit {
			fields[name] = "Ответ не длиннее 1000 символов"
		}
	}
	if len(fields) > 0 {
		return f, httpx.Validation("Проверь ответы", fields)
	}
	if f == (AttemptFeedback{}) {
		return f, httpx.BadRequest("Ответь хотя бы на один вопрос")
	}
	return f, nil
}

// SaveFeedback сохраняет ответы ученика на опрос после урока.
//
// Ответы дописываются к прежним: пропущенный сейчас вопрос не стирает
// ответ, данный в прошлый раз. Закрытый урок опросу не мешает — ученик
// отвечает про урок, который уже прошёл.
func (s *Service) SaveFeedback(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, f AttemptFeedback) error {
	f, err := f.validate()
	if err != nil {
		return err
	}
	// Через ownAttempt: чужая попытка — 404, отзыв за другого не оставить.
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return err
	}
	if err := s.store.SaveFeedback(ctx, s.pool, attempt, f); err != nil {
		return httpx.Internal("Не удалось сохранить ответы").Wrap(err)
	}
	return nil
}

// SaveFeedback записывает ответы опроса по попытке, дописывая их к прежним.
func (s *Store) SaveFeedback(ctx context.Context, q pg.Querier, a Attempt, f AttemptFeedback) error {
	_, err := q.Exec(ctx, `
		INSERT INTO learning.attempt_feedback AS fb
			(attempt_id, org_id, student_id, lesson, avatar, lesson_why, avatar_why, unclear)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), $6, $7, $8)
		ON CONFLICT (attempt_id) DO UPDATE SET
			lesson = COALESCE(EXCLUDED.lesson, fb.lesson),
			avatar = COALESCE(EXCLUDED.avatar, fb.avatar),
			lesson_why = COALESCE(NULLIF(EXCLUDED.lesson_why, ''), fb.lesson_why),
			avatar_why = COALESCE(NULLIF(EXCLUDED.avatar_why, ''), fb.avatar_why),
			unclear = COALESCE(NULLIF(EXCLUDED.unclear, ''), fb.unclear)
		WHERE fb.org_id = EXCLUDED.org_id`,
		a.ID, a.OrgID, a.StudentID, f.Lesson, f.Avatar, f.LessonWhy, f.AvatarWhy, f.Unclear)
	if err != nil {
		return fmt.Errorf("learning: не удалось сохранить ответы опроса: %w", err)
	}
	return nil
}
