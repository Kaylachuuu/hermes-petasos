import AppKit

// One executable, three jobs. Run by hand (or from Finder) it is the app. The petasos program it
// starts runs it again to record (--record RATE) and to play (--play), since macOS has no
// arecord or aplay; see Audio.swift.

let arguments = CommandLine.arguments
if arguments.count >= 2 && arguments[1] == "--record" {
    let rate = arguments.count >= 3 ? Double(arguments[2]) ?? 16000 : 16000
    Audio.record(rate: rate)
} else if arguments.count >= 2 && arguments[1] == "--play" {
    Audio.play()
} else {
    let app = NSApplication.shared
    let delegate = AppDelegate()
    app.delegate = delegate
    app.setActivationPolicy(.regular)
    app.run()
}
