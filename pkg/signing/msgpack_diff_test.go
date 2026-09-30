package signing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// TestMsgpackDifferential proves the library-backed encoder is byte-identical
// to the handwritten encoder it replaced (which the reference vectors pinned)
// across every msgpack size-class boundary and randomized nested actions.
func TestMsgpackDifferential(t *testing.T) {
	var cases []string
	ints := []int64{0, 1, 127, 128, 255, 256, 65535, 65536, 1<<32 - 1, 1 << 32, maxSafeInteger,
		-1, -32, -33, -128, -129, -32768, -32769, -1 << 31, -1<<31 - 1, -maxSafeInteger}
	for _, i := range ints {
		cases = append(cases, fmt.Sprintf(`{"v":%d}`, i), fmt.Sprintf(`[%d]`, i))
	}
	for _, n := range []int{0, 1, 31, 32, 255, 256, 65535, 65536} {
		s, _ := json.Marshal(strings.Repeat("é", n/2) + strings.Repeat("x", n%2))
		cases = append(cases, `{"s":`+string(s)+`}`)
		s, _ = json.Marshal(strings.Repeat("a", n))
		cases = append(cases, string(s))
	}
	for _, n := range []int{0, 15, 16, 65535, 65536} {
		var a, o strings.Builder
		a.WriteByte('[')
		o.WriteByte('{')
		for i := 0; i < n; i++ {
			if i > 0 {
				a.WriteByte(',')
				o.WriteByte(',')
			}
			a.WriteString("true")
			fmt.Fprintf(&o, `"k%d":null`, i)
		}
		cases = append(cases, a.String()+"]", o.String()+"}")
	}
	cases = append(cases, `{"z":1,"a":{"y":[false,null,"x"],"b":-5}}`, `"\u0000\ud83d\ude00"`)
	for _, f := range []string{"testdata/reference_vectors.json", "testdata/backend_l1_frontend_vectors.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var vs struct {
			Cases []struct {
				ActionJSON string `json:"action_json"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &vs); err != nil || len(vs.Cases) == 0 {
			t.Fatalf("%s: no vectors (%v)", f, err)
		}
		for _, c := range vs.Cases {
			cases = append(cases, c.ActionJSON)
		}
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		cases = append(cases, randJSON(r, 0))
	}
	for _, c := range cases {
		want, werr := legacyEncodeActionJSON(c)
		got, gerr := EncodeActionJSON(c)
		if (werr == nil) != (gerr == nil) || !bytes.Equal(want, got) {
			t.Fatalf("mismatch for %.80q: legacy=%x,%v new=%x,%v", c, want, werr, got, gerr)
		}
	}
}

func randJSON(r *rand.Rand, depth int) string {
	k := r.Intn(6)
	if depth > 3 {
		k = r.Intn(3)
	}
	switch k {
	case 0:
		return fmt.Sprint(r.Int63n(2*maxSafeInteger) - maxSafeInteger>>uint(r.Intn(53)))
	case 1:
		b, _ := json.Marshal(strings.Repeat("s", r.Intn(300)))
		return string(b)
	case 2:
		return []string{"true", "false", "null"}[r.Intn(3)]
	case 3, 4:
		n := r.Intn(20)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`"f%d":%s`, i, randJSON(r, depth+1))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	n := r.Intn(20)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = randJSON(r, depth+1)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
