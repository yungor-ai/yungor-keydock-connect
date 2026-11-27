//go:build darwin && arm64 && cgo

#import <Foundation/Foundation.h>
#import <Security/Security.h>
#import <SystemConfiguration/SystemConfiguration.h>
#include <fcntl.h>
#include <stdbool.h>
#include <unistd.h>
#include <xpc/xpc.h>
#include "_cgo_export.h"

static const char *serviceName = "ai.yungor.keydock-connect.tun";

static void discardInactiveConnection(xpc_connection_t connection) {
    xpc_connection_set_event_handler(connection, ^(xpc_object_t event) {});
    xpc_connection_activate(connection);
    xpc_connection_cancel(connection);
}

char *kdTUNCopyRequirement(void) {
    @autoreleasepool {
        SecCodeRef self = NULL;
        SecStaticCodeRef binary = NULL;
        CFDictionaryRef signing = NULL;
        char *requirement = NULL;
        if (SecCodeCopySelf(kSecCSDefaultFlags, &self) == errSecSuccess &&
            SecCodeCopyStaticCode(self, kSecCSDefaultFlags, &binary) == errSecSuccess &&
            SecCodeCopySigningInformation(binary, kSecCSSigningInformation, &signing) == errSecSuccess) {
            NSString *team = ((__bridge NSDictionary *)signing)[(__bridge NSString *)kSecCodeInfoTeamIdentifier];
            NSCharacterSet *valid = [NSCharacterSet characterSetWithCharactersInString:@"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"];
            if ([team isKindOfClass:NSString.class] && team.length == 10 &&
                [team rangeOfCharacterFromSet:valid.invertedSet].location == NSNotFound) {
                NSString *expression = [NSString stringWithFormat:
                    @"identifier \"ai.yungor.keydock-connect\" and anchor apple generic and "
                    "certificate leaf[subject.OU] = \"%@\" and "
                    "certificate 1[field.1.2.840.113635.100.6.2.6] exists and "
                    "certificate leaf[field.1.2.840.113635.100.6.1.13] exists", team];
                requirement = strdup(expression.UTF8String);
            }
        }
        if (signing) CFRelease(signing);
        if (binary) CFRelease(binary);
        if (self) CFRelease(self);
        return requirement;
    }
}

bool kdTUNConsoleUser(uid_t uid) {
    uid_t console = 0;
    CFStringRef user = SCDynamicStoreCopyConsoleUser(NULL, &console, NULL);
    if (!user) return false;
    CFRelease(user);
    return uid != 0 && uid == console;
}

void *kdTUNListen(const char *requirement) {
    @autoreleasepool {
        NSString *peerRequirement = [NSString stringWithUTF8String:requirement];
        xpc_connection_t listener = xpc_connection_create_mach_service(serviceName, NULL, XPC_CONNECTION_MACH_SERVICE_LISTENER);
        if (!listener) return NULL;
        if (xpc_connection_set_peer_code_signing_requirement(listener, peerRequirement.UTF8String) != 0) {
            discardInactiveConnection(listener);
            return NULL;
        }
        xpc_connection_set_event_handler(listener, ^(xpc_object_t event) {
            if (xpc_get_type(event) != XPC_TYPE_CONNECTION) return;
            xpc_connection_t peer = (xpc_connection_t)event;
            if (xpc_connection_set_peer_code_signing_requirement(peer, peerRequirement.UTF8String) != 0) {
                xpc_connection_cancel(peer);
                return;
            }
            xpc_connection_set_event_handler(peer, ^(xpc_object_t message) {
                if (xpc_get_type(message) != XPC_TYPE_DICTIONARY) return;
                xpc_object_t reply = xpc_dictionary_create_reply(message);
                if (!reply) return;
                xpc_connection_t sender = xpc_dictionary_get_remote_connection(message);
                if (!sender) return;
                uid_t uid = xpc_connection_get_euid(sender);
                bool accepted = false;
                if (kdTUNConsoleUser(uid)) {
                    if (xpc_dictionary_get_bool(message, "registration")) {
                        NSBundle *bundle = NSBundle.mainBundle;
                        const char *path = xpc_dictionary_get_string(message, "bundle");
                        const char *version = xpc_dictionary_get_string(message, "version");
                        NSArray *args = NSProcessInfo.processInfo.arguments;
                        NSString *registered = args.count == 3 ? args.lastObject : @"";
                        BOOL sameBundle = path && [bundle.bundleURL.URLByResolvingSymlinksInPath.path isEqualToString:@(path)];
                        BOOL current = version && [registered isEqualToString:@(version)]
                            && [registered isEqual:[bundle objectForInfoDictionaryKey:@"CFBundleVersion"]];
                        xpc_dictionary_set_int64(reply, "registration", !sameBundle ? 4 : current ? 1 : 2);
                        xpc_connection_send_message(sender, reply);
                        return;
                    }
                    int fd = xpc_dictionary_dup_fd(message, "lease");
                    if (fd >= 0) {
                        accepted = fcntl(fd, F_SETFD, FD_CLOEXEC) != -1 && kdTUNEnqueue(fd, uid);
                        if (!accepted) close(fd);
                    }
                }
                xpc_dictionary_set_bool(reply, "accepted", accepted);
                xpc_connection_send_message(sender, reply);
            });
            xpc_connection_resume(peer);
        });
        xpc_connection_resume(listener);
        return (__bridge_retained void *)listener;
    }
}

void kdTUNStopListening(void *handle) {
    xpc_connection_t listener = (__bridge_transfer xpc_connection_t)handle;
    xpc_connection_cancel(listener);
}

int kdTUNRequest(int fd, const char *requirement) {
    @autoreleasepool {
        xpc_connection_t peer = xpc_connection_create_mach_service(serviceName, NULL, XPC_CONNECTION_MACH_SERVICE_PRIVILEGED);
        if (!peer) return false;
        if (xpc_connection_set_peer_code_signing_requirement(peer, requirement) != 0) {
            discardInactiveConnection(peer);
            return false;
        }
        xpc_connection_set_event_handler(peer, ^(xpc_object_t event) {});
        xpc_connection_resume(peer);
        xpc_object_t request = xpc_dictionary_create(NULL, NULL, 0);
        if (fd >= 0) {
            xpc_dictionary_set_fd(request, "lease", fd);
        } else {
            if (!kdTUNConsoleUser(geteuid())) {
                xpc_connection_cancel(peer);
                return 0;
            }
            NSBundle *bundle = NSBundle.mainBundle;
            NSString *version = [bundle objectForInfoDictionaryKey:@"CFBundleVersion"];
            if (!version.length) {
                xpc_connection_cancel(peer);
                return 0;
            }
            xpc_dictionary_set_bool(request, "registration", true);
            xpc_dictionary_set_string(request, "bundle", bundle.bundleURL.URLByResolvingSymlinksInPath.path.UTF8String);
            xpc_dictionary_set_string(request, "version", version.UTF8String);
        }
        dispatch_semaphore_t ready = dispatch_semaphore_create(0);
        __block int result = 0;
        xpc_connection_send_message_with_reply(peer, request, NULL, ^(xpc_object_t response) {
            if (xpc_get_type(response) == XPC_TYPE_DICTIONARY) {
                if (fd >= 0) result = xpc_dictionary_get_bool(response, "accepted") ? 1 : 0;
                else if (xpc_dictionary_get_value(response, "registration")) result = (int)xpc_dictionary_get_int64(response, "registration");
                // v0.4.6 only knows the "lease" request. The signed old service
                // replies accepted=false to the read-only registration query.
                else if (xpc_dictionary_get_value(response, "accepted")) result = 3;
            }
            dispatch_semaphore_signal(ready);
        });
        bool received = dispatch_semaphore_wait(ready, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC)) == 0;
        xpc_connection_cancel(peer);
        return received ? result : 0;
    }
}
