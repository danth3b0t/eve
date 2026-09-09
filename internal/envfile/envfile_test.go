package envfile

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplyLossless(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		values            map[string]string
	}{
		{
			name:   "CRLF quoted export comments and unmanaged secrets",
			input:  "# local settings\r\nexport PORT \t= '3000'  # frontend\r\nPRIVATE=`line one\r\nPORT=9999\r\n$(not-executed)`\r\nOTHER=3000\r\nQUOTED=\"escaped \\\"quote\\\" # literal\"\r\nLITERAL='${HOME}'\r\n",
			want:   "# local settings\r\nexport PORT \t= 32400  # frontend\r\nPRIVATE=`line one\r\nPORT=9999\r\n$(not-executed)`\r\nOTHER=3000\r\nQUOTED=\"escaped \\\"quote\\\" # literal\"\r\nLITERAL='${HOME}'\r\nA_URL=http://[::1]:32400\r\nZ_EMPTY=\r\n",
			values: map[string]string{"PORT": "32400", "Z_EMPTY": "", "A_URL": "http://[::1]:32400"},
		},
		{name: "missing final newline", input: "# private\nPRIVATE=secret-sentinel", want: "# private\nPRIVATE=secret-sentinel\nPORT=20000\n", values: map[string]string{"PORT": "20000"}},
		{name: "empty RHS padding", input: "PORT=   # keep\nEMPTY=\t\n", want: "PORT=20000   # keep\nEMPTY=x\t\n", values: map[string]string{"PORT": "20000", "EMPTY": "x"}},
		{name: "multiline managed value", input: "PORT=\"old\nvalue\" # keep\nOTHER=secret-sentinel\n", want: "PORT=20000 # keep\nOTHER=secret-sentinel\n", values: map[string]string{"PORT": "20000"}},
		{name: "unmanaged duplicate definitions", input: "PRIVATE=a\nPRIVATE=b\nPORT=5#note\n", want: "PRIVATE=a\nPRIVATE=b\nPORT=20000#note\n", values: map[string]string{"PORT": "20000"}},
		{name: "uninterpreted text and extended unmanaged key", input: "notes without quotes\nA.B='two\nlines'\n", want: "notes without quotes\nA.B='two\nlines'\nPORT=20000\n", values: map[string]string{"PORT": "20000"}},
		{name: "BOM and mixed line endings", input: "\ufeffPORT=7\r\nPRIVATE=x\n", want: "\ufeffPORT=20000\r\nPRIVATE=x\nURL=https://example.test\r\n", values: map[string]string{"PORT": "20000", "URL": "https://example.test"}},
		{name: "new file", input: "", want: "A=one\nZ=two\n", values: map[string]string{"Z": "two", "A": "one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Parse([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			out, err := doc.Apply(tc.values)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Fatalf("image mismatch\ngot  %q\nwant %q", out, tc.want)
			}
			again, err := Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := again.Apply(tc.values)
			if err != nil || !bytes.Equal(out, repeated) {
				t.Fatalf("application not idempotent: %v", err)
			}
			original, err := doc.Apply(nil)
			if err != nil || string(original) != tc.input {
				t.Fatal("document was mutated")
			}
		})
	}
}

func TestRejectAmbiguousInputWithoutDisclosure(t *testing.T) {
	for _, input := range []string{
		"PORT='secret-sentinel", "PRIVATE=`secret-sentinel\nPORT=3\n", "PORT=\"secret-sentinel\"suffix\n",
		"PORT=prefix'secret-sentinel\n", "PORT: secret-sentinel\n", "unknown 'secret-sentinel\n",
		"PORT=secret-sentinel\\#ambiguous\n", "PORT=secret-sentinel\\\n", "PORT=secret-sentinel\rNEXT=2",
		"PORT=secret-sentinel\x00", "PORT=secret-sentinel\xff",
	} {
		doc, err := Parse([]byte(input))
		if err == nil || doc != nil {
			t.Fatalf("ambiguous input accepted: %q", input)
		}
		if strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("parser error disclosed a value")
		}
	}
}

func TestDuplicateManagedKeyReportsOriginalLines(t *testing.T) {
	doc, err := Parse([]byte("PORT=secret-sentinel\nPRIVATE='multi\nline'\nexport PORT=other-secret\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := doc.Apply(map[string]string{"PORT": "23456", "NEW": "value"})
	var e *Error
	if out != nil || !errors.As(err, &e) || e.Code != "E_ENV_DUPLICATE" || !reflect.DeepEqual(e.Lines, []int{1, 4}) {
		t.Fatalf("wrong duplicate diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("duplicate error disclosed a value")
	}
}

func TestGeneratedSubset(t *testing.T) {
	for _, value := range []string{"", "23456", "dev:calm-cow-456|opaque-token", "http://[::1]:23456/a?b=c&d=e", "_-.:/@%=+!(),;<>[]^&*?{}~|"} {
		if err := ValidateValue(value); err != nil {
			t.Fatalf("valid portable value rejected: %q: %v", value, err)
		}
	}
	for _, value := range []string{"secret sentinel", "secret\tsentinel", "secret\nsentinel", "secret\rsentinel", "secret'sentinel", "secret\"sentinel", "secret`sentinel", "secret\\sentinel", "secret$sentinel", "secret#sentinel", "secret\x7fsentinel", "secreté", "\xff"} {
		doc, err := Parse([]byte("PORT=1\n"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := doc.Apply(map[string]string{"PORT": "2", "URL": value})
		if err == nil || out != nil {
			t.Fatalf("unsafe generated value accepted: %q", value)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatal("serialization error disclosed the value")
		}
	}
	for _, key := range []string{"", "1KEY", "A.B", "A-B", "A\nB", "é"} {
		doc, _ := Parse(nil)
		if out, err := doc.Apply(map[string]string{key: "value"}); err == nil || out != nil {
			t.Fatalf("invalid managed key accepted: %q", key)
		}
	}
}

func TestLimitsAndInputOwnership(t *testing.T) {
	data := []byte("PORT=1\n")
	doc, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	data[0] = 'X'
	out, err := doc.Apply(map[string]string{"PORT": "2"})
	if err != nil || string(out) != "PORT=2\n" {
		t.Fatal("parser retained mutable caller storage")
	}
	if d, err := Parse(bytes.Repeat([]byte{'#'}, MaxBytes+1)); err == nil || d != nil {
		t.Fatal("oversize input accepted")
	}
	full := []byte("Z=" + strings.Repeat("x", MaxBytes-3) + "\n")
	doc, err = Parse(full)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := doc.Apply(map[string]string{"A": "y"}); err == nil || out != nil {
		t.Fatal("oversize output accepted")
	}
	out, err = doc.Apply(map[string]string{"A": "y", "Z": "x"})
	if err != nil || string(out) != "Z=x\nA=y\n" {
		t.Fatalf("final image fits despite growing an earlier sorted key: %v", err)
	}
}

func FuzzDocument(f *testing.F) {
	for _, input := range []string{"", "PORT=3000\n", "export X='multi\nline' # comment\r\n", "A=`$(not-executed)`\n", "EVE_FUZZ_PORT=1\nEVE_FUZZ_PORT=2\n"} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		doc, err := Parse(input)
		if err != nil {
			return
		}
		original, err := doc.Apply(nil)
		if err != nil || !bytes.Equal(original, input) {
			t.Fatal("unchanged application was not byte-identical")
		}
		values := map[string]string{"EVE_FUZZ_PORT": "23456"}
		out, err := doc.Apply(values)
		if err != nil {
			if out != nil {
				t.Fatal("partial output on error")
			}
			return
		}
		again, err := Parse(out)
		if err != nil {
			t.Fatal("writer emitted unparseable dotenv")
		}
		out2, err := again.Apply(values)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatal("application was not idempotent")
		}
	})
}
