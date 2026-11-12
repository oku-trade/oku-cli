package api

// TriggerOrderType uses protojson enum names, not the exchange's tp/sl strings.
type TriggerOrderType string

const (
	TriggerOrderTypeUnspecified TriggerOrderType = "TRIGGER_ORDER_TYPE_UNSPECIFIED"
	TriggerOrderTypeTakeProfit  TriggerOrderType = "TRIGGER_ORDER_TYPE_TAKE_PROFIT"
	TriggerOrderTypeStopLoss    TriggerOrderType = "TRIGGER_ORDER_TYPE_STOP_LOSS"
)

// ModifyActionMetadata replaces orders using Hyperliquid batchModify.
type ModifyActionMetadata struct {
	Modifies []ModifyOrderMetadata `json:"modifies"`
}

// ModifyOrderMetadata is a complete replacement order. Cloid retains optional
// presence so an explicit empty client order ID is distinct from omission.
type ModifyOrderMetadata struct {
	Coin       string           `json:"coin"`
	Oid        Int64            `json:"oid"`
	Size       string           `json:"size"`
	LimitPx    string           `json:"limitPx,omitempty"`
	TriggerPx  string           `json:"triggerPx,omitempty"`
	Tpsl       TriggerOrderType `json:"tpsl,omitempty"`
	IsBuy      bool             `json:"isBuy"`
	IsMarket   bool             `json:"isMarket"`
	Cloid      *string          `json:"cloid,omitempty"`
	Tif        string           `json:"tif,omitempty"`
	ReduceOnly bool             `json:"reduceOnly,omitempty"`
}

type TWAPActionMetadata struct {
	Coin                 string `json:"coin"`
	Side                 string `json:"side"`
	Sz                   string `json:"sz"`
	DurationMinutes      int    `json:"durationMinutes"`
	Randomize            bool   `json:"randomize"`
	ReduceOnly           bool   `json:"reduceOnly"`
	TriggerPx            string `json:"triggerPx,omitempty"`
	TriggerActivateAbove bool   `json:"triggerActivateAbove,omitempty"`
	StopPx               string `json:"stopPx,omitempty"`
}

type CancelTWAPActionMetadata struct {
	Coin   string `json:"coin"`
	TwapID Int64  `json:"twapId"`
}

type ScaleActionMetadata struct {
	Coin        string `json:"coin"`
	Side        string `json:"side"`
	Sz          string `json:"sz"`
	StartPx     string `json:"startPx"`
	EndPx       string `json:"endPx"`
	TotalOrders int    `json:"totalOrders"`
	SizeSkew    string `json:"sizeSkew"`
	Tif         string `json:"tif,omitempty"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
}

// TrailingStopActionMetadata specifies either a percentage or price retracement.
type TrailingStopActionMetadata struct {
	Coin           string `json:"coin"`
	Side           string `json:"side"`
	Sz             string `json:"sz"`
	ReduceOnly     bool   `json:"reduceOnly"`
	ActivationPx   string `json:"activationPx,omitempty"`
	RetracementPct string `json:"retracementPct,omitempty"`
	RetracementPx  string `json:"retracementPx,omitempty"`
}

type CancelOrderItem struct {
	Coin string `json:"coin"`
	Oid  Int64  `json:"oid"`
}

type CancelOrdersActionMetadata struct {
	Orders []CancelOrderItem `json:"orders"`
}

type ConvertToUsdcActionMetadata struct {
	Coin        string `json:"coin"`
	Sz          string `json:"sz"`
	SlippageBps int    `json:"slippageBps,omitempty"`
}
