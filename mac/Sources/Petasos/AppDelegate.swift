import AppKit
import WebKit

// The app: one window showing the Petasos screen, served by the petasos program it starts in
// the background. Sign-in and settings are that program's own pages, the same ones the Windows
// app shows.
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate, WKUIDelegate {
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
        startEngine() // until it's signed in, it serves only its sign-in page
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
        webView.uiDelegate = self
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
        appMenu.addItem(withTitle: "Settings…", action: #selector(showSettings), keyEquivalent: ",")
        appMenu.addItem(withTitle: "Sign In Again…", action: #selector(signInAgain), keyEquivalent: "")
        appMenu.addItem(withTitle: "Show Log", action: #selector(showLog), keyEquivalent: "")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "Hide Petasos", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(withTitle: "Quit Petasos", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        // Without an Edit menu, copy and paste don't reach the settings page's text fields.
        let editItem = NSMenuItem()
        main.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        editItem.submenu = editMenu
        let windowItem = NSMenuItem()
        main.addItem(windowItem)
        let windowMenu = NSMenu(title: "Window")
        windowMenu.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        windowItem.submenu = windowMenu
        NSApp.mainMenu = main
    }

    // The settings page is the petasos program's own, the same one the Windows app shows.
    @objc private func showSettings() {
        webView.load(URLRequest(url: pageURL.appendingPathComponent("settings.html")))
    }

    // The settings page's "Import a .conf file…" button.
    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = parameters.allowsMultipleSelection
        panel.canChooseDirectories = false
        panel.message = "Choose the WireGuard configuration (.conf) for this Mac"
        panel.beginSheetModal(for: window) { response in
            completionHandler(response == .OK ? panel.urls : nil)
        }
    }

    @objc private func showLog() {
        NSWorkspace.shared.open(support.appendingPathComponent("petasos.log"))
    }

    // MARK: signing in

    @objc private func signInAgain() {
        webView.load(URLRequest(url: pageURL.appendingPathComponent("signin.html")))
    }

    // MARK: the petasos program

    private var engineURL: URL? { Bundle.main.url(forResource: "petasos-engine", withExtension: nil) }

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
