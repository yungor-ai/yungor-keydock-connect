//go:build darwin && arm64 && cgo

#import <ServiceManagement/ServiceManagement.h>
#include <stdbool.h>

static SMAppService *proxyService(void) {
    return [SMAppService daemonServiceWithPlistName:@"ai.yungor.keydock-connect.tun.plist"];
}

// 0 = unregistered, 1 = enabled, 2 = waiting for approval, 3 = missing.
int kdProxyServiceStatus(void) {
    @autoreleasepool {
        switch (proxyService().status) {
            case SMAppServiceStatusEnabled: return 1;
            case SMAppServiceStatusRequiresApproval: return 2;
            case SMAppServiceStatusNotRegistered: return 0;
            default: return 3;
        }
    }
}

void kdOpenProxyApproval(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [SMAppService openSystemSettingsLoginItems];
    });
}

static char *registrationError(NSString *stage, NSError *error) {
    NSString *message = [NSString stringWithFormat:@"%@：%@（%@ %ld）", stage, error.localizedDescription,
                         error.domain, (long)error.code];
    return strdup(message.UTF8String);
}

static NSError *registerService(SMAppService *service) {
    NSError *error = nil;
    if ([service registerAndReturnError:&error] || service.status == SMAppServiceStatusRequiresApproval) return nil;
    if (error.code == kSMErrorAlreadyRegistered && service.status == SMAppServiceStatusEnabled) return nil;
    return error;
}

char *kdRegisterProxyService(bool replace) {
    @autoreleasepool {
        SMAppService *service = proxyService();
        NSString *registerFailure = @"无法注册后台服务，请从「应用程序」打开已签名的 KeyDock Connect";
        if (!replace) {
            NSError *error = registerService(service);
            return error ? registrationError(registerFailure, error) : NULL;
        }
        // Completion fires after the running job has been reaped. Always
        // finish registering from that callback, even if the UI wait times out.
        dispatch_semaphore_t finished = dispatch_semaphore_create(0);
        __block NSError *failure = nil;
        __block NSString *stage = nil;
        [service unregisterWithCompletionHandler:^(NSError *error) {
            if (error) {
                stage = @"无法注销旧后台服务";
                failure = error;
            } else {
                stage = registerFailure;
                failure = registerService(service);
            }
            dispatch_semaphore_signal(finished);
        }];
        if (dispatch_semaphore_wait(finished, dispatch_time(DISPATCH_TIME_NOW, 20 * NSEC_PER_SEC)) != 0)
            return strdup("macOS 仍在更新网络后台组件，请稍后重试。");
        return failure ? registrationError(stage, failure) : NULL;
    }
}
