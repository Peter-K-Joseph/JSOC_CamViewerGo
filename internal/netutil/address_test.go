package netutil

import "testing"

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		name        string
		address     string
		defaultPort int
		wantHost    string
		wantPort    int
	}{
		{
			name:        "bare IP",
			address:     "192.168.1.40",
			defaultPort: 80,
			wantHost:    "192.168.1.40",
			wantPort:    80,
		},
		{
			name:        "full ONVIF URL",
			address:     "http://192.168.1.40:8080/onvif/device_service",
			defaultPort: 80,
			wantHost:    "192.168.1.40",
			wantPort:    8080,
		},
		{
			name:        "missing scheme colon",
			address:     "http//192.168.1.40/onvif/device_service",
			defaultPort: 80,
			wantHost:    "192.168.1.40",
			wantPort:    80,
		},
		{
			name:        "bare host with port and path",
			address:     "camera.local:8000/onvif/device_service",
			defaultPort: 80,
			wantHost:    "camera.local",
			wantPort:    8000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHost, gotPort, err := NormalizeHostPort(tc.address, tc.defaultPort)
			if err != nil {
				t.Fatalf("NormalizeHostPort: %v", err)
			}
			if gotHost != tc.wantHost || gotPort != tc.wantPort {
				t.Fatalf("NormalizeHostPort = %q, %d; want %q, %d", gotHost, gotPort, tc.wantHost, tc.wantPort)
			}
		})
	}
}
