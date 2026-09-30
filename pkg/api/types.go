package api

// Field names follow protojson lowerCamelCase of perps.proto.

type Market struct {
	Coin         string `json:"coin"`
	SzDecimals   int    `json:"szDecimals"`
	MaxLeverage  int    `json:"maxLeverage"`
	OnlyIsolated bool   `json:"onlyIsolated,omitempty"`
	AssetID      int    `json:"assetId"`
	MarkPx       string `json:"markPx,omitempty"`
	OraclePx     string `json:"oraclePx,omitempty"`
	MidPx        string `json:"midPx,omitempty"`
	Funding      string `json:"funding,omitempty"`
	OpenInterest string `json:"openInterest,omitempty"`
	PrevDayPx    string `json:"prevDayPx,omitempty"`
	DayNtlVlm    string `json:"dayNtlVlm,omitempty"`
	Premium      string `json:"premium,omitempty"`
	DisplayName  string `json:"displayName,omitempty"`
}

type GetMarketsResponse struct {
	Markets []Market `json:"markets"`
}

type GetMarketPriceResponse struct {
	Coin     string `json:"coin"`
	MarkPx   string `json:"markPx"`
	MidPx    string `json:"midPx"`
	OraclePx string `json:"oraclePx"`
}

type FundingRate struct {
	Time        Int64  `json:"time"`
	FundingRate string `json:"fundingRate"`
	Premium     string `json:"premium"`
}

type GetMarketFundingHistoryResponse struct {
	Rates []FundingRate `json:"rates"`
}

type MarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalNtlPos     string `json:"totalNtlPos"`
	TotalRawUsd     string `json:"totalRawUsd"`
	TotalMarginUsed string `json:"totalMarginUsed"`
}

type Position struct {
	Coin                string `json:"coin"`
	Szi                 string `json:"szi"`
	EntryPx             string `json:"entryPx"`
	PositionValue       string `json:"positionValue"`
	UnrealizedPnl       string `json:"unrealizedPnl"`
	ReturnOnEquity      string `json:"returnOnEquity"`
	LiquidationPx       string `json:"liquidationPx"`
	LeverageType        string `json:"leverageType"`
	LeverageValue       int    `json:"leverageValue"`
	MarginUsed          string `json:"marginUsed"`
	CumFundingAllTime   string `json:"cumFundingAllTime"`
	CumFundingSinceOpen string `json:"cumFundingSinceOpen"`
}

type GetPerpPositionsResponse struct {
	MarginSummary              *MarginSummary `json:"marginSummary"`
	CrossMarginSummary         *MarginSummary `json:"crossMarginSummary"`
	Withdrawable               string         `json:"withdrawable"`
	CrossMaintenanceMarginUsed string         `json:"crossMaintenanceMarginUsed"`
	Positions                  []Position     `json:"positions"`
}

type Order struct {
	Coin             string  `json:"coin"`
	LimitPx          string  `json:"limitPx"`
	Oid              Int64   `json:"oid"`
	Side             string  `json:"side"`
	Sz               string  `json:"sz"`
	Timestamp        Int64   `json:"timestamp"`
	OrigSz           string  `json:"origSz"`
	Cloid            string  `json:"cloid,omitempty"`
	OrderType        string  `json:"orderType"`
	TriggerPx        string  `json:"triggerPx,omitempty"`
	TriggerCondition string  `json:"triggerCondition,omitempty"`
	IsTrigger        bool    `json:"isTrigger"`
	ReduceOnly       bool    `json:"reduceOnly"`
	IsPositionTpsl   bool    `json:"isPositionTpsl"`
	AssetID          int     `json:"assetId"`
	Tif              string  `json:"tif,omitempty"`
	Children         []Order `json:"children,omitempty"`
}

type GetOpenOrdersResponse struct {
	OpenOrders []Order `json:"openOrders"`
}

type SpotBalance struct {
	Coin  string `json:"coin"`
	Token int    `json:"token"`
	Total string `json:"total"`
	Hold  string `json:"hold"`
}

type PerpsBalance struct {
	AccountValue string `json:"accountValue"`
	Withdrawable string `json:"withdrawable"`
	MarginUsed   string `json:"marginUsed"`
}

type PerpsDexBalance struct {
	Dex          string `json:"dex"`
	AccountValue string `json:"accountValue"`
	Withdrawable string `json:"withdrawable"`
	MarginUsed   string `json:"marginUsed"`
}

type GetAccountBalancesResponse struct {
	SpotBalances         []SpotBalance     `json:"spotBalances"`
	SpotUsdcBalance      string            `json:"spotUsdcBalance"`
	SpotUsdcWithdrawable string            `json:"spotUsdcWithdrawable"`
	PerpsBalance         *PerpsBalance     `json:"perpsBalance"`
	Hip3Balances         []PerpsDexBalance `json:"hip3Balances"`
	AccountAbstraction   string            `json:"accountAbstraction"`
}

type GetBuilderFeeStatusResponse struct {
	BuilderFeePpm               int    `json:"builderFeePpm"`
	BuilderFeeApproved          bool   `json:"builderFeeApproved"`
	BuilderAddress              string `json:"builderAddress"`
	BuilderFeeChargedPpm        int    `json:"builderFeeChargedPpm"`
	BuilderFeeApprovalTargetPpm int    `json:"builderFeeApprovalTargetPpm"`
}

type Fill struct {
	Coin          string `json:"coin"`
	Px            string `json:"px"`
	Sz            string `json:"sz"`
	Side          string `json:"side"`
	Time          Int64  `json:"time"`
	Dir           string `json:"dir"`
	ClosedPnl     string `json:"closedPnl"`
	Hash          string `json:"hash"`
	Oid           Int64  `json:"oid"`
	Crossed       bool   `json:"crossed"`
	Fee           string `json:"fee"`
	Tid           Int64  `json:"tid"`
	FeeToken      string `json:"feeToken"`
	StartPosition string `json:"startPosition"`
	BuilderFee    string `json:"builderFee,omitempty"`
	Kind          string `json:"kind,omitempty"`
	TwapID        Int64  `json:"twapId,omitempty"`
}

type GetUserFillsResponse struct {
	Fills []Fill `json:"fills"`
}

// OrderActionMetadata mirrors perps.proto OrderActionMetadata (subset used).
type OrderActionMetadata struct {
	Coin        string `json:"coin"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Sz          string `json:"sz"`
	LimitPx     string `json:"limitPx,omitempty"`
	Tif         string `json:"tif,omitempty"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	SlippageBps int    `json:"slippageBps,omitempty"`
}

// CancelActionMetadata mirrors perps.proto CancelActionMetadata.
type CancelActionMetadata struct {
	Coin string `json:"coin"`
	Oid  Int64  `json:"oid"`
}

type ActionParams struct {
	Order  *OrderActionMetadata  `json:"order,omitempty"`
	Cancel *CancelActionMetadata `json:"cancel,omitempty"`
}

type CreateActionRequest struct {
	Address string       `json:"address"`
	Params  ActionParams `json:"params"`
}

type ActionPayloadResponse struct {
	ActionID   string `json:"actionId"`
	ActionType string `json:"actionType"`
	Status     string `json:"status"`
	ActionJSON string `json:"actionJson"`
	AssetIndex int    `json:"assetIndex"`
	AssetID    int    `json:"assetId"`
}

type EVMSignature struct {
	R string `json:"r"`
	S string `json:"s"`
	V Int64  `json:"v"`
}

type SignedAction struct {
	ActionJSON string       `json:"actionJson"`
	Nonce      Int64        `json:"nonce"`
	Signature  EVMSignature `json:"signature"`
}

type ConfirmActionRequest struct {
	ActionID     string       `json:"actionId"`
	SignedAction SignedAction `json:"signedAction"`
}

type ConfirmActionResponse struct {
	Status       string `json:"status"`
	HlOid        Int64  `json:"hlOid"`
	ErrorMessage string `json:"errorMessage"`
}

type GetActionResponse struct {
	ActionID     string `json:"actionId"`
	ActionType   string `json:"actionType"`
	State        string `json:"state"`
	HlOid        Int64  `json:"hlOid"`
	ErrorMessage string `json:"errorMessage"`
}

type ReconcileActionsResponse struct {
	Actions []GetActionResponse `json:"actions"`
}

// Device service (oku-cli-device-contract.md).

type Device struct {
	DeviceID       string   `json:"deviceId"`
	DeviceName     string   `json:"deviceName"`
	AgentAddress   string   `json:"agentAddress"`
	AccountAddress string   `json:"accountAddress"`
	Scopes         []string `json:"scopes"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"createdAt,omitempty"`
	LastUsedAt     string   `json:"lastUsedAt,omitempty"`
	RevokedAt      string   `json:"revokedAt,omitempty"`
}

type RedeemEnrollmentRequest struct {
	EnrollmentCode string `json:"enrollmentCode"`
	AgentAddress   string `json:"agentAddress"`
	DeviceName     string `json:"deviceName"`
	Signature      string `json:"signature"`
}

type RedeemEnrollmentResponse struct {
	EnrollmentID        string `json:"enrollmentId"`
	PollSecret          string `json:"pollSecret"`
	ConfirmationCode    string `json:"confirmationCode"`
	ExpiresAt           string `json:"expiresAt"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
}

type PollEnrollmentRequest struct {
	EnrollmentID string `json:"enrollmentId"`
	PollSecret   string `json:"pollSecret"`
}

type PollEnrollmentResponse struct {
	Status         string   `json:"status"`
	DeviceID       string   `json:"deviceId"`
	DeviceToken    string   `json:"deviceToken"`
	Scopes         []string `json:"scopes"`
	AccountAddress string   `json:"accountAddress"`
}

type DeviceResponse struct {
	Device Device `json:"device"`
}
