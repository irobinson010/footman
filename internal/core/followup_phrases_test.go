package core

import "testing"

func TestParseAnswer(t *testing.T) {
	for text, want := range map[string]Answer{
		"yes": AnswerYes, "Yes.": AnswerYes, "yes please": AnswerYes, "Yes, please.": AnswerYes,
		"do it": AnswerYes, "go ahead": AnswerYes, "Confirm.": AnswerYes,
		"no": AnswerNo, "No.": AnswerNo, "cancel": AnswerNo, "don't": AnswerNo, "Don’t.": AnswerNo,
		"yes delete everything": AnswerNone, "no way": AnswerNone, "yes yes": AnswerNone, "": AnswerNone, "...": AnswerNone,
		"I said yes": AnswerNone, "go": AnswerNone,
	} {
		if got := ParseAnswer(text); got != want {
			t.Errorf("ParseAnswer(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestEndsWithQuestion(t *testing.T) {
	for text, want := range map[string]bool{
		"Which city?": true, "It is sunny. Which city? ": true, `He said "which city?"`: true, "**Which city?**": true,
		"It is sunny.": false, "Which city? It is sunny.": false, "": false, "?": true,
	} {
		if got := endsWithQuestion(text); got != want {
			t.Errorf("endsWithQuestion(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestClosesFollowUp(t *testing.T) {
	for text, want := range map[string]bool{
		"never mind": true, "Never mind.": true, "nevermind": true, "that's all": true, "That’s all.": true, "cancel": true,
		"never mind the weather": false, "that is not all": false, "": false,
	} {
		if got := closesFollowUp(text); got != want {
			t.Errorf("closesFollowUp(%q) = %v, want %v", text, got, want)
		}
	}
}
