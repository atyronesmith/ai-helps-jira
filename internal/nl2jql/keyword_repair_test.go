package nl2jql

import (
	"errors"
	"testing"
)

func TestKeywordRepairPreservesIdentifiersAndValues(t *testing.T) {
	for _, input := range []string{
		`how many of my open items`,
		`HOW MANY of my open items`,
		`my open items`,
		`open bugs`,
		`list my open items in Opne ordered by created`,
		`list items assigned to Origianl ordered by created`,
		`list items assigned to opne@example.com ordered by created`,
		`list items assigned to "Opne" ordered by created`,
		`list my items in "Origianl Project" by created`,
		`list my items with priority Critcal ordered by created`,
		`list my items with "opne origianl" ordered by created`,
		`list my items with Öpen ordered by created`,
		`LiSt mY OpEn ItEmS bY sToRy PoInTs`,
		`list my open items by origiXXnal estimate`,
	} {
		t.Run(input, func(t *testing.T) {
			got, err := repairSearchKeywords(input)
			if err != nil || got != input {
				t.Fatalf("repair=%q err=%v; want unchanged %q", got, err, input)
			}
		})
	}
}

func TestKeywordRepairUsesSearchContextAndSingleEdits(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"lsit my opne items by stroy pionts", "list my open items by story points"},
		{"ist my open work items by the logged work time", "list my open work items by the logged work time"},
		{"list my open items orderd by origianl estimate", "list my open items ordered by original estimate"},
		{"list items assignd to Opne ordered by remaning estimate", "list items assigned to Opne ordered by remaining estimate"},
		{"list my open items in Origianl by remaning estimate", "list my open items in Origianl by remaining estimate"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := repairSearchKeywords(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("repair=%q err=%v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestKeywordRepairRelativePriorityTypoIsNarrow(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"list all my open jiras thare are priority critical and order by priority", "list all my open jiras that are priority critical and order by priority"},
		{"list my open JIRAS ThArE Is priority critical", "list my open JIRAS that Is priority critical"},
		{"list my open jiras that are priority critical", "list my open jiras that are priority critical"},
		{"list items in Thare", "list items in Thare"},
		{"list items assigned to Thare", "list items assigned to Thare"},
		{`list items "thare" are priority critical`, `list items "thare" are priority critical`},
		{"find my open items that contain the words thare", "find my open items that contain the words thare"},
		{"list my open items thare critical", "list my open items thare critical"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := repairSearchKeywords(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("repair=%q err=%v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestAmbiguousKeywordRepairRequiresClarification(t *testing.T) {
	// Both asc and desc are one edit away; the direction must not be guessed.
	input := "list my open items by original estimate dasc"
	if _, err := repairSearchKeywords(input); !errors.Is(err, ErrAmbiguousIntent) {
		t.Fatalf("repair error=%v; want clarification", err)
	}
	if _, err := ParsePlan(input, "", NewNormalizer().clock()()); !errors.Is(err, ErrAmbiguousIntent) {
		t.Fatalf("plan error=%v; want clarification", err)
	}
}
