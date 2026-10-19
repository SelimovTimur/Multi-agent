package content

import (
	"encoding/json"
	"testing"
)

func TestDraftWarningInputDistinguishesKeepingAndClearingWarnings(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"title":"Урок"}`, ""},
		{`{"title":"Урок","quality_warnings":[]}`, "[]"},
		{`{"title":"Урок","quality_warnings":[{"stage":"frank_text","message":"Осталось другое замечание"}]}`,
			`[{"stage":"frank_text","message":"Осталось другое замечание"}]`},
	} {
		var in draftInput
		if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
			t.Fatal(err)
		}
		if got := string(in.toDraft().QualityWarnings); got != tc.want {
			t.Fatalf("замечания потеряны: got=%s want=%s", got, tc.want)
		}
	}
}
