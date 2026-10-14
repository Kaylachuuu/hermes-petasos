import AppKit
import WebKit

// The app: one window showing the Petasos screen, served by the petasos program it starts in
// the background. The first time, it asks for the Hermes address and a sign-in.
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private var engine: Process?
    private var quitting = false

    private let pageURL = URL(string: "http://127.0.0.1:8187/")!
    private let support: URL = {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        let dir = base.appendingPathComponent("Petasos", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }()

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildMenu()
        buildWindow()
        NSApp.activate(ignoringOtherApps: true)
        if FileManager.default.fileExists(atPath: support.appendingPathComponent("tokens.json").path) {
            startEngine()
        } else {
            DispatchQueue.main.async { self.signIn() }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationWillTerminate(_ notification: Notification) {
        quitting = true
        engine?.terminate()
    }

    // MARK: window and menu

    private func buildWindow() {
        let config = WKWebViewConfiguration()
        config.suppressesIncrementalRendering = true
        webView = WKWebView(frame: NSRect(x: 0, y: 0, width: 800, height: 544), configuration: config)
        webView.setValue(false, forKey: "drawsBackground") // the page paints its own night colour
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 800, height: 544),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable],
                          backing: .buffered, defer: false)
        window.title = "Petasos"
        window.appearance = NSAppearance(named: .darkAqua)
        window.backgroundColor = NSColor(red: 0x1f / 255.0, green: 0x1b / 255.0, blue: 0x17 / 255.0, alpha: 1)
        window.contentAspectRatio = NSSize(width: 800, height: 544) // the Car Thing's screen, plus the buttons under it
        window.contentMinSize = NSSize(width: 560, height: 381)
        window.contentView = webView
        window.delegate = self
        window.setFrameAutosaveName("Petasos")
        if !window.setFrameUsingName("Petasos") {
            window.center()
        }
        window.makeKeyAndOrderFront(nil)
        window.makeFirstResponder(webView)
    }

    private func buildMenu() {
        let main = NSMenu()
        let appItem = NSMenuItem()
        main.addItem(appItem)
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "About Petasos", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Sign In Again…", action: #selector(signInAgain), keyEquivalent: "")
        appMenu.addItem(withTitle: "Show Log", action: #selector(showLog), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Petasos", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(withTitle: "Quit Petasos", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        let windowItem = NSMenuItem()
        main.addItem(windowItem)
        let windowMenu = NSMenu(title: "Window")
        windowMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        windowItem.submenu = windowMenu
        NSApp.mainMenu = main
    }

    @objc private func showLog() {
        NSWorkspace.shared.open(support.appendingPathComponent("petasos.log"))
    }

    // MARK: signing in

    @objc private func signInAgain() {
        stopEngine()
        signIn()
    }

    private func signIn(error: String? = nil) {
        let alert = NSAlert()
        alert.messageText = "Sign in to Hermes"
        alert.informativeText = error ?? "The same address and sign-in as the Hermes dashboard. Petasos keeps its own renewable sign-in, like the Car Thing."
        if error != nil { alert.alertStyle = .warning }
        let server = NSTextField(string: UserDefaults.standard.string(forKey: "server") ?? "http://10.0.0.20:9119")
        let username = NSTextField(string: UserDefaults.standard.string(forKey: "username") ?? "")
        let password = NSSecureTextField(string: "")
        server.placeholderString = "Hermes address"
        username.placeholderString = "Username"
        password.placeholderString = "Password"
        let stack = NSStackView(views: [server, username, password])
        stack.orientation = .vertical
        stack.spacing = 8
        stack.frame = NSRect(x: 0, y: 0, width: 280, height: 86)
        for field in [server, username, password] {
            field.widthAnchor.constraint(equalToConstant: 280).isActive = true
        }
        alert.accessoryView = stack
        alert.addButton(withTitle: "Sign In")
        alert.addButton(withTitle: "Quit")
        alert.window.initialFirstResponder = username.stringValue.isEmpty ? username : password
        guard alert.runModal() == .alertFirstButtonReturn else {
            NSApp.terminate(nil)
            return
        }
        UserDefaults.standard.set(server.stringValue, forKey: "server")
        UserDefaults.standard.set(username.stringValue, forKey: "username")
        let (ok, output) = runEngine(["-dir", support.path, "signin", server.stringValue, username.stringValue],
                                     input: password.stringValue + "\n")
        if ok {
            startEngine()
        } else {
            signIn(error: output.isEmpty ? "Signing in didn't work." : output)
        }
    }

    // MARK: the petasos program

    private var engineURL: URL? { Bundle.main.url(forResource: "petasos-engine", withExtension: nil) }

    // runEngine runs the petasos program once and returns whether it worked, and what it said.
    private func runEngine(_ args: [String], input: String) -> (Bool, String) {
        guard let url = engineURL else { return (false, "The petasos program is missing from the app.") }
        let process = Process()
        process.executableURL = url
        process.arguments = args
        let stdin = Pipe(), stdout = Pipe()
        process.standardInput = stdin
        process.standardOutput = stdout
        process.standardError = stdout
        do {
            try process.run()
        } catch {
            return (false, "Couldn't start the petasos program: \(error.localizedDescription)")
        }
        stdin.fileHandleForWriting.write(input.data(using: .utf8)!)
        try? stdin.fileHandleForWriting.close()
        let said = String(data: stdout.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        process.waitUntilExit()
        return (process.terminationStatus == 0, said.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    private func startEngine() {
        guard let url = engineURL, let me = Bundle.main.executablePath else { return }
        let logURL = support.appendingPathComponent("petasos.log")
        FileManager.default.createFile(atPath: logURL.path, contents: nil)
        let log = try? FileHandle(forWritingTo: logURL)
        let process = Process()
        process.executableURL = url
        process.arguments = ["-dir", support.path, "-audio", me, "virtual"]
        process.standardOutput = log
        process.standardError = log
        process.terminationHandler = { [weak self] _ in
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
                guard let self = self, !self.quitting, self.engine === process else { return }
                self.startEngine() // it stopped on its own: start it again
            }
        }
        do {
            try process.run()
        } catch {
            let alert = NSAlert()
            alert.messageText = "Couldn't start Petasos"
            alert.informativeText = error.localizedDescription
            alert.runModal()
            return
        }
        engine = process
        waitForPage(tries: 50)
    }

    private func stopEngine() {
        let old = engine
        engine = nil
        old?.terminate()
        old?.waitUntilExit()
    }

    // waitForPage loads the screen once the petasos program serves it.
    private func waitForPage(tries: Int) {
        var request = URLRequest(url: pageURL)
        request.timeoutInterval = 1
        URLSession.shared.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self = self else { return }
                if (response as? HTTPURLResponse)?.statusCode == 200 {
                    self.webView.load(URLRequest(url: self.pageURL))
                    self.window.makeFirstResponder(self.webView)
                } else if tries > 0 {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.2) { self.waitForPage(tries: tries - 1) }
                }
            }
        }.resume()
    }
}
