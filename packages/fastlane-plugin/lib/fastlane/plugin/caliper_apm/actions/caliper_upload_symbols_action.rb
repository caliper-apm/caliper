# frozen_string_literal: true

require_relative "../uploader"

module Fastlane
  module Actions
    # Fastlane lane usage:
    #   caliper_upload_symbols(
    #     server_url: "https://apm.example.com",
    #     api_key: ENV["CALIPER_API_KEY"],
    #     app_id: "dev.caliper.demo",
    #     app_version: "1.1.0",
    #     build_number: "110",
    #     kind: "dsym",
    #     symbol_path: "./build/AtlasDemo.dsym.json"
    #   )
    class CaliperUploadSymbolsAction
      def self.run(params)
        git_sha = params[:git_sha]
        if git_sha.nil? || git_sha.empty?
          detected = `git rev-parse --short=12 HEAD 2>/dev/null`.strip
          git_sha = detected if detected.match?(/\A[a-fA-F0-9]{7,40}\z/)
        end

        uploader = Fastlane::CaliperApm::Uploader.new(
          server_url: params[:server_url],
          api_key: params[:api_key]
        )
        bin_size = params[:binary_size_bytes]
        bin_kind = params[:binary_artifact_kind]
        if params[:binary_path] && File.file?(params[:binary_path])
          bin_size ||= File.size(params[:binary_path])
          ext = File.extname(params[:binary_path]).delete_prefix(".").downcase
          bin_kind ||= ext if %w[apk aab ipa xcarchive bundle].include?(ext)
        end

        uploader.register_release(
          app_id: params[:app_id],
          platform: params[:platform] || "ios",
          env: params[:env] || "prod",
          app_version: params[:app_version],
          build_number: params[:build_number],
          git_sha: git_sha,
          adapter: params[:adapter] || "native",
          binary_size_bytes: bin_size,
          binary_artifact_kind: bin_kind
        )
        uploader.upload_symbol(
          app_id: params[:app_id],
          platform: params[:platform] || "ios",
          env: params[:env] || "prod",
          app_version: params[:app_version],
          build_number: params[:build_number],
          kind: params[:kind] || "dsym",
          file_path: params[:symbol_path],
          git_sha: git_sha
        )
      end

      def self.description
        "Register release metadata and upload iOS dSYM / Breakpad / SourceMap symbols to Caliper APM"
      end

      def self.is_supported?(platform)
        %i[ios android].include?(platform)
      end
    end
  end
end
