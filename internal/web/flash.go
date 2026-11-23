package web

import "net/http"

const flashCookie = "watchpickr_flash"

type flash struct {
	Level string // "ok" | "error"
	Text  string
}

// flashes maps the code in the cookie to its message. Having them all here
// makes translating easy, for when a second language arrives.
// You know... when I have time...
var flashes = map[string]flash{
	"added":    {"ok", "Added to the board. Do not forget to vote for it."},
	"voted":    {"ok", "Vote cast."},
	"unvoted":  {"ok", "Vote taken back."},
	"watched":  {"ok", "Marked as watched. Everyone who backed it has their vote back."},
	"reopened": {"ok", "Back in the running. Previous votes were not restored."},
	"removed":  {"ok", "Removed from the board."},

	"badlink":   {"error", "That does not look like an IMDb link."},
	"notitle":   {"error", "The film needs a title."},
	"dupe":      {"error", "That film is already on the board."},
	"novotes":   {"error", "You have no votes left. Take one back first."},
	"already":   {"error", "You have already voted for that one."},
	"iswatched": {"error", "That film has already been watched."},
	"gone":      {"error", "That film is no longer on the board."},
	"notyours":  {"error", "You can only remove your own suggestions."},
	"hasvotes":  {"error", "That film has votes. Ask an admin to remove it."},
	"oops":      {"error", "Something went wrong. Please try again."},

	"sessionmade":    {"ok", "Session board created. Share the code and people can join."},
	"sessionclosed":  {"ok", "Voting is closed. The result stays visible."},
	"sessiondeleted": {"ok", "Session deleted, along with its guests and their votes. History is kept."},
	"closed":         {"error", "This board is closed. Voting has finished."},
	"nosession":      {"error", "That session no longer exists."},
	"wasclosed":      {"error", "That session was already closed."},
	"noseries":       {"error", "That is a TV series, and series are switched off on this board."},

	"settingssaved":   {"ok", "Settings saved."},
	"trimmed":         {"ok", "Saved. Some people were over the new limit and their most recent votes were removed."},
	"adminchanged":    {"ok", "Administrator rights updated."},
	"userdeleted":     {"ok", "Member removed. Their suggestions stay on the board."},
	"namechanged":     {"ok", "Name changed. It updates everywhere your name appears."},
	"namesame":        {"ok", "That was already your name."},
	"passwordchanged": {"ok", "Password changed. Any other signed-in devices were logged out."},

	"overbudget":  {"error", "Somebody is holding more votes than that. Choose how to handle it, or ask them to release one."},
	"badvotes":    {"error", "The vote budget has to be a whole number that is 1 or more."},
	"lastadmin":   {"error", "That is the last administrator. Promote somebody else first."},
	"notyourself": {"error", "You cannot remove your own account."},
	"nouser":      {"error", "That member no longer exists."},
}

func (s *Server) setFlash(w http.ResponseWriter, code string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    code,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60,
	})
}

// takeFlash reads the message and clears the cookie, so it only shows once.
func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	// Unknown codes are ignored
	if f, ok := flashes[c.Value]; ok {
		return &f
	}
	return nil
}
