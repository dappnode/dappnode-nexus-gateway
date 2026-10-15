package dto

// ErrorResponse is the standard error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error classification fields.
type ErrorDetail struct {
	Type           string `json:"type"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	Origin         string `json:"origin"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
}
