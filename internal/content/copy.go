package content

import (
	"slices"

	"github.com/google/uuid"
)

// Копия в новую версию получает собственные ID. В частности, унаследованные
// цели с ID старой версии иначе нарушают первичный ключ при первой правке.
func freshContent(in DraftContent) DraftContent {
	in.Lexicon = slices.Clone(in.Lexicon)
	in.Grammar = slices.Clone(in.Grammar)
	in.Exercises = slices.Clone(in.Exercises)
	in.Objectives = slices.Clone(in.Objectives)
	for i := range in.Lexicon {
		in.Lexicon[i].ID = uuid.Nil
	}
	for i := range in.Grammar {
		in.Grammar[i].ID = uuid.Nil
	}
	for i := range in.Exercises {
		in.Exercises[i].ID = uuid.Nil
	}
	for i := range in.Objectives {
		in.Objectives[i].ID = uuid.Nil
	}
	return in
}
