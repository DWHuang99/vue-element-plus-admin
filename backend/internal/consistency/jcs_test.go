package consistency

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarshalJCS_RFC8785Appendix pins the canonical serialization to the
// RFC 8785 Appendix A vectors: key ordering, number normalization, escaping.
func TestMarshalJCS_RFC8785Appendix(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{
			name: "A.1 case-sensitive key order",
			in:   map[string]any{"Numbers": "one", "numbers": "two"},
			want: `{"Numbers":"one","numbers":"two"}`,
		},
		{
			// The RFC literal 333333333.33333329 rounds to the same IEEE-754
			// double as 333333333.3333333, whose shortest form is the latter.
			name: "A.2 numbers and literals",
			in: map[string]any{
				"numbers":  []any{333333333.33333329, 1e30, 4.50, 2e-3, 0.000000000000000000000000001},
				"string":   "",
				"literals": []any{nil, true, false},
			},
			want: `{"literals":[null,true,false],"numbers":[333333333.3333333,1e30,4.5,0.002,1e-27],"string":""}`,
		},
		{
			name: "A.3 wide characters pass through unescaped",
			in:   map[string]any{"Unicode": []any{"£", "ก", "แ", "\U0001D11E"}},
			want: `{"Unicode":["£","ก","แ","𝄞"]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MarshalJCS(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

// TestMarshalJCS_KeyOrderUTF16 pins the code-unit ordering rule: case
// sensitivity, astral keys as surrogate pairs, BMP keys before astral ones
// whose high surrogate sorts later, and surrogate-vs-E000 boundaries.
func TestMarshalJCS_KeyOrderUTF16(t *testing.T) {
	got, err := MarshalJCS(map[string]any{
		"Z":          true,
		"a":          true,
		"\uE000":     true, // private use, sorts after any surrogate high unit
		"\U0001D11E": true, // high surrogate 0xD834
		"\U00010020": true, // high surrogate 0xD800
		"\U00010000": true, // high surrogate 0xD800, low 0xDC00
		"":           true,
	})
	require.NoError(t, err)
	// UTF-16 order: "" < "Z" (0x5A) < "a" (0x61) < U+10000 [D800,DC00]
	// < U+10020 [D800,DC20] < U+1D11E [D834,DD1E] < U+E000 (0xE000).
	want := "{\"\":true,\"Z\":true,\"a\":true," +
		"\"\U00010000\":true,\"\U00010020\":true,\"\U0001D11E\":true,\"\uE000\":true}"
	assert.Equal(t, want, string(got))
}

// TestMarshalJCS_StringEscaping: only the JSON metacharacters and control
// characters are escaped, with the shortest allowed forms; U+2028/2029,
// HTML metacharacters, DEL and C1 controls are literal.
func TestMarshalJCS_StringEscaping(t *testing.T) {
	s := "\"\\\b\t\n\f\r\x00\x01\x1f\u2028\u2029<>&\u007F\u0080\u009F£"
	got, err := MarshalJCS(map[string]any{"s": s})
	require.NoError(t, err)
	want := `{"s":"\"\\\b\t\n\f\r` + "\\u0000\\u0001\\u001f\u2028\u2029<>&\u007F\u0080\u009F£" + `"}`
	assert.Equal(t, want, string(got))
}

// TestMarshalJCS_NumberNormalization: RFC 8785 §3.2.2.1 shapes.
func TestMarshalJCS_NumberNormalization(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, `0`},
		{math.Copysign(0, -1), `0`}, // negative zero is zero
		{1.0, `1`},                  // integral floats are integers
		{-2.5, `-2.5`},
		{1e20, `100000000000000000000`}, // 21 digits: decimal form
		{1e21, `1e21`},                  // 22 digits: exponent required
		{2e-3, `0.002`},                 // decimal form possible: no exponent
		{0.000001, `0.000001`},          // 1e-6 boundary stays decimal
		{9.99999e-7, `9.99999e-7`},      // below 1e-6: exponent, no zero padding
		{1e-27, `1e-27`},
		{333333333.33333329, `333333333.3333333`},
		{4.5, `4.5`},
	}
	for _, tc := range tests {
		got, err := MarshalJCS(map[string]any{"n": tc.in})
		require.NoError(t, err)
		assert.Equal(t, `{"n":`+tc.want+`}`, string(got), "input %v", tc.in)
	}
}

// TestMarshalJCS_Errors: values outside the supported set must fail loudly.
func TestMarshalJCS_Errors(t *testing.T) {
	bad := []any{
		math.NaN(),
		math.Inf(1),
		map[int]any{1: "x"},
		[]any{struct{}{}},
		[]byte("x"),
		1 + 2i,
	}
	for _, v := range bad {
		_, err := MarshalJCS(map[string]any{"bad": v})
		assert.Error(t, err, "input %#v", v)
	}
	// invalid UTF-8 inside a string must fail, not silently transform
	_, err := MarshalJCS(map[string]any{"s": "a\xffb"})
	assert.Error(t, err, "invalid UTF-8")
}
