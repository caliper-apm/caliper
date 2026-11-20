# frozen_string_literal: true

require "minitest/autorun"
require "socket"
require "tempfile"
require "zlib"
require "stringio"
require "digest"
require_relative "../lib/fastlane/plugin/caliper_apm/actions/caliper_upload_symbols_action"

class CaliperFastlaneUploaderTest < Minitest::Test
  def test_register_and_gzip_symbol_upload
    server = TCPServer.new("127.0.0.1", 0)
    port = server.addr[1]
    received = []

    thread = Thread.new do
      2.times do
        client = server.accept
        request_line = client.gets
        headers = {}
        while (line = client.gets) && line != "\r\n"
          k, v = line.split(":", 2)
          headers[k.downcase.strip] = v.strip if k && v
        end
        body = client.read(headers["content-length"].to_i)
        received << { line: request_line.strip, headers: headers, body: body }

        resp_body = request_line.include?("/v1/symbols/upload") ? '{"symbol_id":"sym-ios-1"}' : '{"status":"registered"}'
        status = request_line.include?("/v1/symbols/upload") ? "201 Created" : "200 OK"
        client.write("HTTP/1.1 #{status}\r\nContent-Type: application/json\r\nContent-Length: #{resp_body.bytesize}\r\nConnection: close\r\n\r\n#{resp_body}")
        client.close
      end
    end

    tmp = Tempfile.new(["atlas", ".dsym.json"])
    payload_str = '{"entries":[{"address":"0x1040","symbol":"ImageDecoder.decodeFrame(_:)","file":"ImageDecoder.swift","line":84}]}'
    tmp.write(payload_str)
    tmp.flush

    receipt = Fastlane::Actions::CaliperUploadSymbolsAction.run(
      server_url: "http://127.0.0.1:#{port}",
      api_key: "ios-ci-key",
      app_id: "dev.caliper.demo",
      platform: "ios",
      env: "prod",
      app_version: "1.1.0",
      build_number: "110",
      git_sha: "1100002abcdef",
      kind: "dsym",
      symbol_path: tmp.path
    )

    thread.join(5)
    server.close
    tmp.close!

    assert_equal 2, received.length
    assert_includes received[0][:line], "POST /v1/releases/register"
    assert_equal "ios-ci-key", received[0][:headers]["x-caliper-key"]

    assert_includes received[1][:line], "POST /v1/symbols/upload"
    assert_equal "gzip", received[1][:headers]["content-encoding"]
    decompressed = Zlib::GzipReader.new(StringIO.new(received[1][:body])).read
    assert_equal payload_str, decompressed
    assert_equal Digest::SHA256.hexdigest(payload_str), received[1][:headers]["x-caliper-sha256"]
    assert_equal 201, receipt[:status]
    assert_equal "sym-ios-1", receipt[:body]["symbol_id"]
  end
end
