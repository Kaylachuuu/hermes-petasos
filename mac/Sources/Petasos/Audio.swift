import AVFoundation
import Foundation

// The recorder and player the petasos program uses on the Mac, in place of the Car Thing's
// arecord and aplay. Both run as their own short-lived process.
enum Audio {
    // record writes the default microphone to stdout as raw 32-bit little-endian mono samples at
    // the given rate (what arecord -f S32_LE -c 1 gives on the Car Thing), until it is stopped.
    static func record(rate: Double) -> Never {
        signal(SIGPIPE, SIG_IGN) // a closed pipe then shows up as a failed write, and ends the recording
        checkAccess()
        let recorder = Recorder(rate: rate)
        // The engine stops when the microphone changes under it, such as a Bluetooth headset
        // switching to its call mode as recording starts; start again with the new one.
        NotificationCenter.default.addObserver(forName: .AVAudioEngineConfigurationChange,
                                               object: recorder.engine, queue: .main) { _ in
            recorder.restart()
        }
        // petasos asks it to stop with SIGTERM at the end of each turn: close the microphone
        // properly first, so AirPods aren't left half switched for the next one.
        signal(SIGTERM, SIG_IGN)
        let stop = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
        stop.setEventHandler {
            recorder.stop()
            exit(0)
        }
        stop.resume()
        stopSource = stop
        recorder.start()
        dispatchMain()
    }

    private static var stopSource: DispatchSourceSignal?

    static func fail(_ message: String) -> Never {
        FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
        exit(1)
    }

    // checkAccess makes sure macOS lets Petasos use the microphone, asking the first time.
    // Without access the microphone gives nothing, so say why in the log instead.
    private static func checkAccess() {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            return
        case .notDetermined:
            let asked = DispatchSemaphore(value: 0)
            var granted = false
            AVCaptureDevice.requestAccess(for: .audio) { ok in
                granted = ok
                asked.signal()
            }
            asked.wait()
            if granted { return }
        default:
            break
        }
        fail("macOS isn't letting Petasos use the microphone (System Settings > Privacy & Security > Microphone)")
    }

    // play plays a WAV file from stdin and returns when it has finished.
    static func play() -> Never {
        let data = FileHandle.standardInput.readDataToEndOfFile()
        guard let player = try? AVAudioPlayer(data: data) else {
            FileHandle.standardError.write("can't read the audio to play\n".data(using: .utf8)!)
            exit(1)
        }
        player.prepareToPlay()
        guard player.play() else { exit(1) }
        while player.isPlaying {
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        exit(0)
    }
}

// Recorder is the microphone, converted to the rate petasos asked for.
final class Recorder {
    let engine = AVAudioEngine()
    private let rate: Double
    private let outFormat: AVAudioFormat
    private var restarts = 0

    init(rate: Double) {
        self.rate = rate
        guard let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: rate,
                                         channels: 1, interleaved: false) else {
            Audio.fail("can't record at \(rate) Hz")
        }
        outFormat = format
    }

    func start() {
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, inFormat.channelCount > 0,
              let converter = AVAudioConverter(from: inFormat, to: outFormat) else {
            Audio.fail("no microphone to record from (input format \(inFormat))")
        }
        let rate = self.rate, outFormat = self.outFormat
        let out = FileHandle.standardOutput
        input.installTap(onBus: 0, bufferSize: 4096, format: inFormat) { buffer, _ in
            let capacity = AVAudioFrameCount(Double(buffer.frameLength) * rate / inFormat.sampleRate) + 32
            guard let converted = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: capacity) else { return }
            var handedOver = false
            var error: NSError?
            converter.convert(to: converted, error: &error) { _, status in
                if handedOver {
                    status.pointee = .noDataNow
                    return nil
                }
                handedOver = true
                status.pointee = .haveData
                return buffer
            }
            let count = Int(converted.frameLength)
            guard error == nil, count > 0, let samples = converted.floatChannelData?[0] else { return }
            var data = Data(count: count * 4)
            data.withUnsafeMutableBytes { raw in
                let words = raw.bindMemory(to: Int32.self)
                for i in 0..<count {
                    let v = max(-1.0, min(1.0, Double(samples[i])))
                    words[i] = Int32(v * 2147483647.0).littleEndian
                }
            }
            do {
                try out.write(contentsOf: data)
            } catch {
                exit(0)
            }
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            Audio.fail("can't start the microphone: \(error)")
        }
    }

    func stop() {
        engine.stop()
        engine.inputNode.removeTap(onBus: 0)
    }

    func restart() {
        restarts += 1
        if restarts > 5 {
            Audio.fail("the microphone keeps changing; giving up")
        }
        FileHandle.standardError.write("the microphone changed; starting it again\n".data(using: .utf8)!)
        stop()
        start()
    }
}
