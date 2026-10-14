package main

import (
	"strings"
	"testing"
)

// The shape busctl --json=short gives for BlueZ's GetManagedObjects, trimmed.
const managedObjects = `{"type":"a{oa{sa{sv}}}","data":[{
 "/org/bluez/hci0":{"org.bluez.Adapter1":{"Powered":{"type":"b","data":true}}},
 "/org/bluez/hci0/dev_E8_98_EE_DC_6D_AE":{"org.bluez.Device1":{
   "Address":{"type":"s","data":"E8:98:EE:DC:6D:AE"},"Alias":{"type":"s","data":"Kayla's iPhone"},
   "Paired":{"type":"b","data":true},
   "UUIDs":{"type":"as","data":["0000110a-0000-1000-8000-00805f9b34fb","00001116-0000-1000-8000-00805f9b34fb"]}}},
 "/org/bluez/hci0/dev_11_22_33_44_55_66":{"org.bluez.Device1":{
   "Address":{"type":"s","data":"11:22:33:44:55:66"},"Alias":{"type":"s","data":"Headphones"},
   "Paired":{"type":"b","data":true},
   "UUIDs":{"type":"as","data":["0000110b-0000-1000-8000-00805f9b34fb"]}}},
 "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF":{"org.bluez.Device1":{
   "Address":{"type":"s","data":"AA:BB:CC:DD:EE:FF"},"Alias":{"type":"s","data":"Stranger"},
   "Paired":{"type":"b","data":false},
   "UUIDs":{"type":"as","data":["00001116-0000-1000-8000-00805f9b34fb"]}}}
}]}`

func TestHotspotPhones(t *testing.T) {
	got, err := pairedOffering([]byte(managedObjects), napUUID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Kayla's iPhone" || got[0].Path != "/org/bluez/hci0/dev_E8_98_EE_DC_6D_AE" {
		t.Fatalf("got %+v", got)
	}
	if got, _ := pairedOffering([]byte(managedObjects), napUUID, "e8:98:ee:dc:6d:ae"); len(got) != 1 {
		t.Fatalf("preferred: got %+v", got)
	}
	speakers, _ := pairedOffering([]byte(managedObjects), audioSinkUUID, "")
	if len(speakers) != 1 || speakers[0].Name != "Headphones" || speakers[0].Connected {
		t.Fatalf("speakers: got %+v", speakers)
	}
}

func TestComputersShareInternetAndOneGoesFirst(t *testing.T) {
	mac := strings.Replace(managedObjects, `"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF":{"org.bluez.Device1":{
   "Address":{"type":"s","data":"AA:BB:CC:DD:EE:FF"},"Alias":{"type":"s","data":"Stranger"},
   "Paired":{"type":"b","data":false},`, `"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF":{"org.bluez.Device1":{
   "Address":{"type":"s","data":"AA:BB:CC:DD:EE:FF"},"Alias":{"type":"s","data":"MacBook"},
   "Paired":{"type":"b","data":true},"Icon":{"type":"s","data":"computer"},`, 1)
	mac = strings.Replace(mac, `"data":["00001116-0000-1000-8000-00805f9b34fb"]}}}
}]}`, `"data":[]}}}
}]}`, 1)
	got, err := pairedOffering([]byte(mac), napUUID, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("a paired computer can share internet: %+v %v", got, err)
	}
	if got := firstOf(got, "e8:98:ee:dc:6d:ae"); got[0].Name != "Kayla's iPhone" {
		t.Fatalf("the phone first: %+v", got)
	}
	if got := firstOf(got, "AA:BB:CC:DD:EE:FF"); got[0].Name != "MacBook" || len(got) != 2 {
		t.Fatalf("the MacBook first: %+v", got)
	}
}

const wgQuick = `[Interface]
PrivateKey = cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MDA=
Address = 10.8.0.7/24, fd00::7/64
DNS = 10.0.0.1   # ignored

[Peer]
PublicKey = cHVibGlja2V5cHVibGlja2V5cHVibGlja2V5cHViMDA=
AllowedIPs = 10.0.0.0/24, 10.8.0.0/24
Endpoint = home.example.net:51820
PersistentKeepalive = 25
`

func TestWireGuard(t *testing.T) {
	cfg, err := parseWireGuard([]byte(wgQuick))
	if err != nil {
		t.Fatal(err)
	}
	netdev, network := cfg.networkd()
	for _, want := range []string{"Kind=wireguard", "RouteTable=main",
		"PrivateKey=cHJpdmF0ZWtleXByaXZhdGVrZXlwcml2YXRla2V5MDA=", "AllowedIPs=10.0.0.0/24, 10.8.0.0/24",
		"Endpoint=home.example.net:51820", "PersistentKeepalive=25"} {
		if !strings.Contains(string(netdev), want) {
			t.Errorf("netdev lacks %q:\n%s", want, netdev)
		}
	}
	if !strings.Contains(string(network), "Address=10.8.0.7/24\nAddress=fd00::7/64\n") {
		t.Errorf("network:\n%s", network)
	}
	full := strings.Replace(wgQuick, "AllowedIPs = 10.0.0.0/24, 10.8.0.0/24", "AllowedIPs = 0.0.0.0/0", 1)
	if _, err := parseWireGuard([]byte(full)); err == nil || !strings.Contains(err.Error(), "everything home") {
		t.Errorf("full tunnel: %v", err)
	}
	if _, err := parseWireGuard([]byte("[Interface]\nAddress = 10.8.0.7/24\n")); err == nil {
		t.Error("missing key accepted")
	}
}
