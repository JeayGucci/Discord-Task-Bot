package channels

type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Group struct {
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name"`
	Channels []Channel `json:"channels"`
}
