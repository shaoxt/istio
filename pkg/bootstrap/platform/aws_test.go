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
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

type handlerFunc func(http.ResponseWriter, *http.Request)

func TestAWSLocality(t *testing.T) {
	cases := []struct {
		name     string
		handlers map[string]handlerFunc
		want     *core.Locality
	}{
		{
			"error",
			map[string]handlerFunc{"/placement/region": errorHandler, "/placement/availability-zone": errorHandler},
			&core.Locality{},
		},
		{
			"locality",
			map[string]handlerFunc{"/placement/region": regionHandler, "/placement/availability-zone": zoneHandler},
			&core.Locality{Region: "us-west-2", Zone: "us-west-2b"},
		},
	}

	for _, v := range cases {
		t.Run(v.name, func(tt *testing.T) {
			tt.Setenv(EnvAWSRegion, "")
			tt.Setenv(EnvAWSAvailabilityZone, "")
			tt.Setenv(EnvNodeName, "")
			server, url := setupHTTPServer(v.handlers)
			defer server.Close()
			awsMetadataIPv4URL = url.String()
			locality := NewAWS(false).Locality()
			if !reflect.DeepEqual(locality, v.want) {
				t.Errorf("unexpected locality. want :%v, got :%v", v.want, locality)
			}
		})
	}
}

func TestIsAWS(t *testing.T) {
	cases := []struct {
		name     string
		handlers map[string]handlerFunc
		want     bool
	}{
		{"not aws", map[string]handlerFunc{"/iam/info": errorHandler}, false},
		{"aws", map[string]handlerFunc{"/iam/info": iamInfoHandler}, true},
	}

	for _, v := range cases {
		t.Run(v.name, func(tt *testing.T) {
			tt.Setenv(EnvAWSRegion, "")
			tt.Setenv(EnvAWSAvailabilityZone, "")
			tt.Setenv(EnvNodeName, "")
			server, url := setupHTTPServer(v.handlers)
			defer server.Close()
			awsMetadataIPv4URL = url.String()
			aws := IsAWS(false)
			if !reflect.DeepEqual(aws, v.want) {
				t.Errorf("unexpected iam info. want :%v, got :%v", v.want, aws)
			}
		})
	}
}

func TestIsAWSWhenAWSRegionEnvSet(t *testing.T) {
	t.Setenv(EnvAWSRegion, "us-west-2")
	if !IsAWS(false) {
		t.Fatal("expected IsAWS true when AWS_REGION is set")
	}
}

func TestNewAWSMetadataFromEnvSkipsIMDS(t *testing.T) {
	t.Setenv(EnvAWSRegion, "us-west-2")
	t.Setenv(EnvAWSAvailabilityZone, "us-west-2a")
	t.Setenv(EnvNodeName, "ip-10-0-0-1.ec2.internal")

	server, url := setupHTTPServer(map[string]handlerFunc{
		"/placement/region":            errorHandler,
		"/placement/availability-zone": errorHandler,
		"/instance-id":                 errorHandler,
	})
	defer server.Close()
	awsMetadataIPv4URL = url.String()

	e := NewAWS(false)
	loc := e.Locality()
	want := &core.Locality{Region: "us-west-2", Zone: "us-west-2a"}
	if !reflect.DeepEqual(loc, want) {
		t.Errorf("unexpected locality. want :%v, got :%v", want, loc)
	}
	md := e.Metadata()
	if md[AWSRegion] != "us-west-2" || md[AWSAvailabilityZone] != "us-west-2a" || md[AWSInstanceID] != "ip-10-0-0-1.ec2.internal" {
		t.Errorf("unexpected metadata: %v", md)
	}
}

func TestNewAWSMetadataFromEnvUsesK8SNodeName(t *testing.T) {
	t.Setenv(EnvAWSRegion, "us-west-2")
	t.Setenv(EnvAWSAvailabilityZone, "us-west-2b")
	t.Setenv(EnvNodeName, "node-from-k8s")

	e := NewAWS(false)
	if e.Metadata()[AWSInstanceID] != "node-from-k8s" {
		t.Errorf("expected K8S_NODE_NAME as instance id, got %v", e.Metadata())
	}
}

// When only some AWS metadata env vars are set, NewAWS must merge from IMDS for the rest (no panic).
func TestNewAWSPartialEnvOnlyRegionMergesFromIMDS(t *testing.T) {
	t.Setenv(EnvAWSRegion, "us-west-2")
	t.Setenv(EnvAWSAvailabilityZone, "")
	t.Setenv(EnvNodeName, "")

	server, u := setupHTTPServer(map[string]handlerFunc{
		"/placement/availability-zone": zoneHandler,
		"/instance-id":                 ec2InstanceIDHandler,
	})
	defer server.Close()
	awsMetadataIPv4URL = u.String()

	e := NewAWS(false)
	want := &core.Locality{Region: "us-west-2", Zone: "us-west-2b"}
	if got := e.Locality(); !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected locality. want %v, got %v", want, got)
	}
	md := e.Metadata()
	if got := md[AWSInstanceID]; got != "i-imdsinstance" {
		t.Errorf("expected instance id from IMDS, got %q (metadata=%v)", got, md)
	}
}

func TestNewAWSPartialEnvRegionAndZoneMergesInstanceFromIMDS(t *testing.T) {
	t.Setenv(EnvAWSRegion, "eu-central-1")
	t.Setenv(EnvAWSAvailabilityZone, "eu-central-1a")
	t.Setenv(EnvNodeName, "")

	server, u := setupHTTPServer(map[string]handlerFunc{
		"/placement/region":            errorHandler,
		"/placement/availability-zone": errorHandler,
		"/instance-id":                 ec2InstanceIDHandler,
	})
	defer server.Close()
	awsMetadataIPv4URL = u.String()

	e := NewAWS(false)
	want := &core.Locality{Region: "eu-central-1", Zone: "eu-central-1a"}
	if got := e.Locality(); !reflect.DeepEqual(got, want) {
		t.Errorf("unexpected locality. want %v, got %v", want, got)
	}
	md := e.Metadata()
	if got := md[AWSInstanceID]; got != "i-imdsinstance" {
		t.Errorf("expected instance id from IMDS, got %q (metadata=%v)", got, md)
	}
}

func errorHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusInternalServerError)
}

func regionHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("us-west-2"))
}

func zoneHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("us-west-2b"))
}

func iamInfoHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	// nolint: lll
	writer.Write([]byte("{\n\"Code\" : \"Success\",\n\"LastUpdated\" : \"2022-03-18T05:04:31Z\",\n\"InstanceProfileArn\" : \"arn:aws:iam::614624372165:instance-profile/sam-processing0000120190916053337315200000004\",\n\"InstanceProfileId\" : \"AIPAY6GTXUXC3LLJY7OG7\"\n\t  }"))
}

func ec2InstanceIDHandler(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	writer.Write([]byte("i-imdsinstance"))
}

func setupHTTPServer(handlers map[string]handlerFunc) (*httptest.Server, *url.URL) {
	handler := http.NewServeMux()
	for path, handle := range handlers {
		handler.HandleFunc(path, handle)
	}
	server := httptest.NewServer(handler)
	url, _ := url.Parse(server.URL)
	return server, url
}
