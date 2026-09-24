package tron

type nowBlockResponse struct {
	BlockHeader struct {
		RawData struct {
			Number    int64 `json:"number"`
			Timestamp int64 `json:"timestamp"`
		} `json:"raw_data"`
	} `json:"block_header"`
}

type contractEventsResponse struct {
	Success bool               `json:"success"`
	Data    []contractEvent    `json:"data"`
	Meta    contractEventsMeta `json:"meta"`
}

type contractEventsMeta struct {
	Fingerprint string `json:"fingerprint"`
	PageSize    int    `json:"page_size"`
}

type contractEvent struct {
	BlockNumber     int64             `json:"block_number"`
	BlockTimestamp  int64             `json:"block_timestamp"`
	EventName       string            `json:"event_name"`
	Result          map[string]string `json:"result"`
	TransactionID   string            `json:"transaction_id"`
	ContractAddress string            `json:"contract_address"`
}
