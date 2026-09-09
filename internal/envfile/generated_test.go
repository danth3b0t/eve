package envfile

import "testing"

func TestGeneratedValueIsNotGeneralEvaluation(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"export PORT = 3000 # user comment\n", "3000", true},
		{"PORT=\n", "", true},
		{"PORT='3000'\n", "", false},
		{"PORT=${OTHER}\n", "", false},
		{"PORT=1\nPORT=2\n", "", false},
		{"OTHER=1\n", "", false},
	} {
		doc, err := Parse([]byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		got, err := doc.GeneratedValue("PORT")
		if tc.valid {
			if err != nil || got != tc.want {
				t.Fatalf("generated read: %v", err)
			}
		} else if err == nil {
			t.Fatal("missing, ambiguous or nonportable generated assignment accepted")
		}
	}
}
