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
            name: "CaliperSDK",
            dependencies: []
        ),
        .testTarget(
            name: "CaliperSDKTests",
            dependencies: ["CaliperSDK"]
        ),
    ]
)
