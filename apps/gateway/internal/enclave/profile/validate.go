package profile

import (
	"fmt"
	"regexp"
)

var (
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-\d+$`)
	kmsARNPattern   = regexp.MustCompile(`^arn:(?:aws|aws-us-gov|aws-cn):kms:[a-z0-9-]+:[0-9]{12}:key/[0-9a-fA-F-]{36}$`)
)

// ValidateBuild checks values compiled into the measured enclave binary.
func ValidateBuild() error {
	if !revisionPattern.MatchString(SourceRevision) {
		return fmt.Errorf("enclave source revision must be a full 40-character lowercase Git commit SHA")
	}
	if !regionPattern.MatchString(AWSRegion) {
		return fmt.Errorf("enclave AWS region is invalid")
	}
	if !kmsARNPattern.MatchString(KMSKeyARN) {
		return fmt.Errorf("enclave KMS key ARN must identify a customer-managed key")
	}
	return nil
}
