package api

type GetTwapHistoryRequest struct {
	Address   string `json:"address"`
	SkipCache bool   `json:"skipCache,omitempty"`
}

type GetTwapHistoryResponse struct {
	Twaps []PerpTwap `json:"twaps"`
}

// PerpTwap is a lifecycle entry. Time is in seconds, Timestamp in milliseconds.
type PerpTwap struct {
	TwapID               Int64  `json:"twapId"`
	Time                 Int64  `json:"time"`
	Status               string `json:"status"`
	Coin                 string `json:"coin"`
	User                 string `json:"user"`
	Side                 string `json:"side"`
	Sz                   string `json:"sz"`
	Minutes              int    `json:"minutes"`
	ReduceOnly           bool   `json:"reduceOnly"`
	Randomize            bool   `json:"randomize"`
	Timestamp            Int64  `json:"timestamp"`
	Leverage             int    `json:"leverage"`
	ExecutedSz           string `json:"executedSz"`
	ExecutedNtl          string `json:"executedNtl"`
	TriggerPx            string `json:"triggerPx,omitempty"`
	TriggerActivateAbove bool   `json:"triggerActivateAbove,omitempty"`
	StopPx               string `json:"stopPx,omitempty"`
}
