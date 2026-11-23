// swift-tools-version: 5.10
import PackageDescription

let package = Package(
    name: "CaliperSDK",
    platforms: [
        .iOS(.v15),
        .macOS(.v13),
    ],
    products: [
        .library(
            name: "CaliperSDK",
            targets: ["CaliperSDK"]
        ),
    ],
    targets: [
        .target(
            name: "CaliperCrashCore",
            dependencies: [],
            publicHeadersPath: "include"
        ),
        .target(
            name: "CaliperSDK",
            dependencies: ["CaliperCrashCore"]
        ),
        .testTarget(
            name: "CaliperSDKTests",
            dependencies: ["CaliperSDK"]
        ),
    ]
)
