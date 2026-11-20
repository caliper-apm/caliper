// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "AtlasDemoHost",
    platforms: [.iOS(.v15), .macOS(.v14)],
    products: [
        .library(name: "AtlasDemo", targets: ["AtlasDemo"]),
    ],
    dependencies: [
        .package(path: "../../packages/sdk-ios"),
    ],
    targets: [
        .target(
            name: "AtlasDemo",
            dependencies: [
                .product(name: "CaliperSDK", package: "sdk-ios"),
            ],
            path: "Sources/AtlasDemo"
        ),
    ]
)
