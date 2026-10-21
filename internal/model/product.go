package model

import "time"

type Product struct {
	Key               string `json:"key"`
	Provider          string `json:"provider"`
	Category          string `json:"category"`
	PlanName          string `json:"planName"`
	RAM               string `json:"ram,omitempty"`
	CPU               string `json:"cpu,omitempty"`
	Storage           string `json:"storage,omitempty"`
	Bandwidth         string `json:"bandwidth,omitempty"`
	Price             string `json:"price"`
	BillingPeriod     string `json:"billingPeriod"`
	AvailableQuantity int    `json:"availableQuantity"`
	OrderURL          string `json:"orderUrl"`
	SourceURL         string `json:"sourceUrl"`
}

type Issue struct {
	SourceURL string `json:"sourceUrl"`
	Product   string `json:"product,omitempty"`
	Error     string `json:"error"`
}

type Inventory struct {
	Products   []Product `json:"products"`
	Categories int       `json:"categories"`
	Issues     []Issue   `json:"issues,omitempty"`
}

type ActiveRestock struct {
	ChatID           int64     `json:"chatId"`
	MessageID        int64     `json:"messageId"`
	RestockStartedAt time.Time `json:"restockStartedAt"`
	Delivered        Product   `json:"delivered"`
	// A witnessed zero must close this event even if stock returns before an edit succeeds.
	SoldOut   *Product  `json:"soldOut,omitempty"`
	SoldOutAt time.Time `json:"soldOutAt,omitempty"`
}

type PendingSend struct {
	StartedAt  time.Time `json:"startedAt"`
	RetryAfter time.Time `json:"retryAfter"`
}

type ProductState struct {
	Product       Product        `json:"product"`
	LastAvailable int            `json:"lastAvailable"`
	LastCheckedAt time.Time      `json:"lastCheckedAt"`
	ActiveRestock *ActiveRestock `json:"activeRestock"`
	PendingSend   *PendingSend   `json:"pendingSend,omitempty"`
}

type State struct {
	Version  int                      `json:"version"`
	Products map[string]*ProductState `json:"products"`
}

func NewState() State { return State{Version: 1, Products: make(map[string]*ProductState)} }
