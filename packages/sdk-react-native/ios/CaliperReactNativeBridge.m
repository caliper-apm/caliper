#if __has_include(<React/RCTBridgeModule.h>)
#import <React/RCTBridgeModule.h>

@interface RCT_EXTERN_MODULE(CaliperReactNativeBridge, NSObject)

RCT_EXTERN_METHOD(setScene:(NSString *)scene)
RCT_EXTERN_METHOD(addBreadcrumb:(NSString *)category message:(NSString *)message)
RCT_EXTERN_METHOD(notifyFirstFrameReady:(double)jsEntryToFirstFrameMs)
RCT_EXTERN_METHOD(enqueueEvent:(NSString *)kind
                  scene:(NSString *)scene
                  windowId:(NSString * _Nullable)windowId
                  payloadJson:(NSString *)payloadJson)
RCT_EXTERN_METHOD(flush)

@end
#endif
