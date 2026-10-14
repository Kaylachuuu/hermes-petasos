#!/bin/sh
# Build Petasos.app here, from the Swift app and the macOS builds of the petasos program in
# ../client/dist. Needs the Xcode command line tools (xcode-select --install), not Go.
set -e
cd "$(dirname "$0")"
v=$(cat ../client/VERSION)
swift build -c release
bin=$(swift build -c release --show-bin-path)

app=Petasos.app
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin/Petasos" "$app/Contents/MacOS/Petasos"
lipo -create ../client/dist/petasos-darwin-arm64 ../client/dist/petasos-darwin-amd64 \
    -output "$app/Contents/Resources/petasos-engine"
chmod +x "$app/Contents/Resources/petasos-engine"
[ -f Petasos.icns ] && cp Petasos.icns "$app/Contents/Resources/"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>Petasos</string>
    <key>CFBundleDisplayName</key><string>Petasos</string>
    <key>CFBundleIdentifier</key><string>com.kaylachuuu.petasos</string>
    <key>CFBundleExecutable</key><string>Petasos</string>
    <key>CFBundleIconFile</key><string>Petasos</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>$v</string>
    <key>CFBundleVersion</key><string>$v</string>
    <key>LSMinimumSystemVersion</key><string>13.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSMicrophoneUsageDescription</key><string>Petasos listens while you hold the talk key, to send what you say to your agent.</string>
    <key>NSAppleEventsUsageDescription</key><string>Petasos changes the Mac's volume when you use the volume keys.</string>
    <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
PLIST

# Signed for this Mac only, which is all macOS needs to run it and to remember the microphone
# permission.
codesign --force -s - "$app/Contents/Resources/petasos-engine"
codesign --force -s - "$app"
echo "Built $(pwd)/$app ($v). Drag it to Applications, or open it from here."
