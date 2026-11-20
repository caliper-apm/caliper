# frozen_string_literal: true

require "digest"
require "json"
require "net/http"
require "stringio"
require "uri"
require "zlib"

module Fastlane
  module CaliperApm
    VERSION = "0.5.0"

    # Zero-dependency Ruby uploader for iOS dSYM / Breakpad / Hermes SourceMap bundles.
    class Uploader
      def initialize(server_url:, api_key: nil, open_timeout: 10, read_timeout: 30)
        @server_url = server_url.to_s.sub(%r{/+$}, "")
        @api_key = api_key.to_s
        @open_timeout = open_timeout
        @read_timeout = read_timeout
      end

      def register_release(app_id:, platform: "ios", env: "prod", app_version:, build_number:, git_sha: nil, adapter: "native")
        uri = URI.parse("#{@server_url}/v1/releases/register")
        payload = {
          app_id: app_id,
          platform: platform,
          env: env,
          app_version: app_version,
          build_number: build_number,
          adapter: adapter
        }
        payload[:git_sha] = git_sha if git_sha && !git_sha.empty?

        req = Net::HTTP::Post.new(uri)
        req["Content-Type"] = "application/json"
        req["X-Caliper-Key"] = @api_key unless @api_key.empty?
        req.body = JSON.generate(payload)

        resp = perform(uri, req)
        raise "Caliper release register failed: HTTP #{resp.code} #{resp.body}" unless resp.code.to_i.between?(200, 299)

        JSON.parse(resp.body)
      end

      def upload_symbol(app_id:, platform: "ios", env: "prod", app_version:, build_number:, kind:, file_path:, git_sha: nil)
        raise "Symbol file not found: #{file_path}" unless File.exist?(file_path)

        raw = File.binread(file_path)
        sha256 = Digest::SHA256.hexdigest(raw)
        gz_io = StringIO.new
        Zlib::GzipWriter.wrap(gz_io) { |gz| gz.write(raw) }
        gz_bytes = gz_io.string

        params = {
          "app_id" => app_id,
          "platform" => platform,
          "env" => env,
          "app_version" => app_version,
          "build_number" => build_number,
          "kind" => kind
        }
        params["git_sha"] = git_sha if git_sha && !git_sha.empty?
        uri = URI.parse("#{@server_url}/v1/symbols/upload?#{URI.encode_www_form(params)}")

        req = Net::HTTP::Post.new(uri)
        req["Content-Type"] = "application/octet-stream"
        req["Content-Encoding"] = "gzip"
        req["X-Caliper-SHA256"] = sha256
        req["X-Caliper-Key"] = @api_key unless @api_key.empty?
        req.body = gz_bytes

        resp = perform(uri, req)
        raise "Caliper symbol upload failed: HTTP #{resp.code} #{resp.body}" unless resp.code.to_i.between?(200, 299)

        {
          status: resp.code.to_i,
          sha256: sha256,
          raw_bytes: raw.bytesize,
          gzip_bytes: gz_bytes.bytesize,
          body: JSON.parse(resp.body)
        }
      end

      private

      def perform(uri, req)
        http = Net::HTTP.new(uri.host, uri.port)
        http.use_ssl = (uri.scheme == "https")
        http.open_timeout = @open_timeout
        http.read_timeout = @read_timeout
        http.request(req)
      end
    end
  end
end
