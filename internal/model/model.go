// Package model — ответы внешних речевых сервисов, общие для их клиентов.
package model

// ASRResponse — ответ asr_service /recognize.
//
// Поля проверки говорят, уверено ли распознавание: при NeedsReview ученик
// видит текст до отправки и может его поправить.
type ASRResponse struct {
	Text     string `json:"text"`
	Language string `json:"language,omitempty"`
	Error    string `json:"error,omitempty"`
	// Confidence — уверенность 0…1; nil — модель её не даёт.
	Confidence  *float64 `json:"confidence,omitempty"`
	NeedsReview bool     `json:"needs_review"`
	// ReviewReasons — low_confidence, no_confidence, foreign_letters,
	// quiet_audio, noisy_audio, clipped_audio.
	ReviewReasons []string `json:"review_reasons,omitempty"`
}
