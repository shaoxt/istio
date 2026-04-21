// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package platform

import (
	"os"
	"strings"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"

	"istio.io/istio/pkg/http"
	"istio.io/istio/pkg/log"
)

const (
	AWSRegion           = "aws_region"
	AWSAvailabilityZone = "aws_availability_zone"
	AWSInstanceID       = "aws_instance_id"

	// EnvAWSRegion is the standard AWS region variable (e.g. set by IRSA on EKS).
	EnvAWSRegion = "AWS_REGION"
	// EnvAWSAvailabilityZone may be set to avoid IMDS calls for zone (e.g. from topology labels).
	EnvAWSAvailabilityZone = "AWS_AVAILABILITY_ZONE"
	// EnvNodeName should be the Kubernetes node name (e.g. from spec.nodeName).
	EnvNodeName = "K8S_NODE_NAME"
)

var (
	awsMetadataIPv4URL = "http://169.254.169.254/latest/meta-data"
	awsMetadataIPv6URL = "http://[fd00:ec2::254]/latest/meta-data"

	awsMetadataTokenIPv4URL = "http://169.254.169.254/latest/api/token"
	awsMetadataTokenIPv6URL = "http://[fd00:ec2::254]/latest/api/token"
)

// Approach derived from the following:
// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/identify_ec2_instances.html
// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html

// IsAWS returns whether the platform for bootstrapping is Amazon Web Services.
func IsAWS(ipv6 bool) bool {
	// When AWS_REGION is set (e.g. EKS with IRSA), treat as AWS without calling IMDS.
	// This avoids IMDSv2 token throttling during large bursts of concurrent pod starts.
	if strings.TrimSpace(os.Getenv(EnvAWSRegion)) != "" {
		return true
	}
	headers := requestHeaders(ipv6)
	info, err := getAWSInfo("iam/info", ipv6, headers)
	return err == nil && strings.Contains(info, "arn:aws:iam")
}

type awsEnv struct {
	region           string
	availabilityZone string
	instanceID       string
}

// NewAWS returns a platform environment customized for AWS.
// Metadata is normally read from the instance metadata service (link-local on the node).
// If AWS_REGION, AWS_AVAILABILITY_ZONE, and K8S_NODE_NAME are all set,
// metadata is taken from the environment and IMDS is not contacted.
func NewAWS(ipv6 bool) Environment {
	region := strings.TrimSpace(os.Getenv(EnvAWSRegion))
	availabilityZone := strings.TrimSpace(os.Getenv(EnvAWSAvailabilityZone))
	instanceID := awsInstanceIDFromEnv()

	if region != "" && availabilityZone != "" && instanceID != "" {
		log.Debug("using AWS region, zone, and instance identity from environment variables; skipping IMDS")
		return &awsEnv{
			region:           region,
			availabilityZone: availabilityZone,
			instanceID:       instanceID,
		}
	}

	headers := requestHeaders(ipv6)
	if region == "" {
		region, _ = getAWSInfo("placement/region", ipv6, headers)
	}
	if availabilityZone == "" {
		availabilityZone, _ = getAWSInfo("placement/availability-zone", ipv6, headers)
	}
	if instanceID == "" {
		instanceID, _ = getAWSInfo("instance-id", ipv6, headers)
	}
	return &awsEnv{
		region:           region,
		availabilityZone: availabilityZone,
		instanceID:       instanceID,
	}
}

func awsInstanceIDFromEnv() string {
	return strings.TrimSpace(os.Getenv(EnvNodeName))
}

func requestHeaders(ipv6 bool) map[string]string {
	// try to get token first, if it fails, fallback to IMDSv1
	token := getToken(ipv6)
	if token == "" {
		log.Debugf("token is empty, will fallback to IMDSv1")
	}

	headers := make(map[string]string, 1)
	if token != "" {
		headers["X-aws-ec2-metadata-token"] = token
	}
	return headers
}

func (a *awsEnv) Metadata() map[string]string {
	md := map[string]string{}
	if len(a.availabilityZone) > 0 {
		md[AWSAvailabilityZone] = a.availabilityZone
	}
	if len(a.region) > 0 {
		md[AWSRegion] = a.region
	}
	if len(a.instanceID) > 0 {
		md[AWSInstanceID] = a.instanceID
	}
	return md
}

func (a *awsEnv) Locality() *core.Locality {
	return &core.Locality{
		Zone:   a.availabilityZone,
		Region: a.region,
	}
}

func (a *awsEnv) Labels() map[string]string {
	return map[string]string{}
}

func (a *awsEnv) IsKubernetes() bool {
	return true
}

func getAWSInfo(path string, ipv6 bool, headers map[string]string) (string, error) {
	url := awsMetadataIPv4URL + "/" + path
	if ipv6 {
		url = awsMetadataIPv6URL + "/" + path
	}

	resp, err := http.GET(url, time.Millisecond*100, headers)
	if err != nil {
		log.Debugf("error in getting aws info for %s : %v", path, err)
		return "", err
	}
	return resp.String(), nil
}

func getToken(ipv6 bool) string {
	url := awsMetadataTokenIPv4URL
	if ipv6 {
		url = awsMetadataTokenIPv6URL
	}

	resp, err := http.PUT(url, time.Millisecond*100, map[string]string{
		// more details can be found at https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instancedata-data-retrieval.html
		"X-aws-ec2-metadata-token-ttl-seconds": "60",
	})
	if err != nil {
		log.Debugf("error in getting aws token : %v", err)
		return ""
	}
	return resp.String()
}
