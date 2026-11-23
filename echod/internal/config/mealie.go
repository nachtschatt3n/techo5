package config

// Mealie is a Mealie server (the recipe manager) this device's screen shows the shopping list, the
// meal plan and the recipes of. Token is a long-lived API token of a Mealie user; it is never shown once
// saved, and the household it belongs to is the one the screen shows.
type Mealie struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
	// List is the shopping list shown, by its Mealie id; empty for the first one the server has.
	List string `json:"list,omitempty"`
	// Sleeps lets the screen dim and go dark while a recipe is open. It is the opposite way round from
	// the setting's name on the page so that an old state.json, which has none, keeps the screen on.
	Sleeps bool `json:"sleeps,omitempty"`
}

// Set is whether there is a server to ask.
func (m Mealie) Set() bool { return m.URL != "" && m.Token != "" }

type MealieWriter struct{ st *Store }

// Server saves the address, and the token unless token is nil: the setup page leaves a saved token
// alone when its field is left empty.
func (w MealieWriter) Server(url string, token *string) error {
	return w.st.Update(func(c *Config) {
		c.Mealie.URL = url
		if token != nil {
			c.Mealie.Token = *token
		}
	})
}

// List saves the shopping list shown, by id; empty for the first one.
func (w MealieWriter) List(id string) error {
	return w.st.Update(func(c *Config) { c.Mealie.List = id })
}

// Sleeps saves whether the screen may go dark while a recipe is open.
func (w MealieWriter) Sleeps(b bool) error {
	return w.st.Update(func(c *Config) { c.Mealie.Sleeps = b })
}
