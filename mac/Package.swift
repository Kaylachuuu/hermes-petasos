// swift-tools-version:5.9
// The virtual Petasos for the Mac. Build the app with ./build.sh, which also puts the petasos
// program (the same one the Car Thing runs, built for macOS) inside it.
import PackageDescription

let package = Package(
    name: "Petasos",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "Petasos", path: "Sources/Petasos")
    ]
)
