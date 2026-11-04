package apitest

type AdminAppsJSON struct {
	Items []struct {
		ID      string `json:"id"`
		CanOpen bool   `json:"canOpen"`
		Owner   struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Username    string `json:"username"`
		} `json:"owner"`
		App struct {
			Name  string `json:"name"`
			Phase string `json:"phase"`
		} `json:"app"`
	} `json:"items"`
}
