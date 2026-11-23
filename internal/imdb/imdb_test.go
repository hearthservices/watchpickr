package imdb

import "testing"

func TestParseID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://www.imdb.com/title/tt4654462/", "tt4654462"},
		{"https://m.imdb.com/title/tt4654462/?reg_=fn_al_tt_1", "tt4654462"},
		{"tt4654462", "tt4654462"},
		{"  tt4654462  ", "tt4654462"},
		{"https://www.imdb.com/", ""},
		{"nonsense", ""},
		{"ttt123", ""},
		{"tt123", ""},
	}
	for _, tt := range tests {
		got, ok := ParseID(tt.in)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("ParseID(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
}
