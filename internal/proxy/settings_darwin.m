//go:build darwin && arm64 && cgo

#import <Foundation/Foundation.h>
#import <SystemConfiguration/SystemConfiguration.h>

// Stored with the preferences it describes, under the same SCPreferences lock
// and commit. Only the launchd service writes this key.
static CFStringRef const receiptKey = CFSTR("ai.yungor.keydock-connect.SystemProxy");

static NSArray<NSString *> *protocols(void) {
    return @[@"HTTP", @"HTTPS", @"SOCKS"];
}

static NSDictionary *ownedFields(int port) {
    NSMutableDictionary *values = [@{@"ProxyAutoConfigEnable": @0, @"ProxyAutoDiscoveryEnable": @0} mutableCopy];
    for (NSString *protocol in protocols()) {
        values[[protocol stringByAppendingString:@"Enable"]] = @1;
        values[[protocol stringByAppendingString:@"Proxy"]] = @"127.0.0.1";
        values[[protocol stringByAppendingString:@"Port"]] = @(port);
    }
    return values;
}

static BOOL matchesOwned(NSDictionary *current, NSDictionary *owned) {
    for (NSString *key in owned) if (![current[key] isEqual:owned[key]]) return NO;
    return YES;
}

static NSDictionary *priorFields(NSDictionary *current, NSDictionary *owned) {
    NSMutableDictionary *prior = [NSMutableDictionary new];
    for (NSString *key in owned) prior[key] = current[key];
    return prior;
}

static NSDictionary *restoredFields(NSDictionary *current, NSDictionary *prior, NSDictionary *owned) {
    NSMutableDictionary *next = [current mutableCopy];
    for (NSString *protocol in protocols()) {
        NSString *host = [protocol stringByAppendingString:@"Proxy"];
        NSString *port = [protocol stringByAppendingString:@"Port"];
        // A changed endpoint belongs to the new owner, including its switch.
        if (![current[host] isEqual:owned[host]] || ![current[port] isEqual:owned[port]]) continue;
        next[host] = prior[host];
        next[port] = prior[port];
        NSString *enabled = [protocol stringByAppendingString:@"Enable"];
        if ([current[enabled] isEqual:owned[enabled]]) next[enabled] = prior[enabled];
    }
    for (NSString *key in @[@"ProxyAutoConfigEnable", @"ProxyAutoDiscoveryEnable"]) {
        if ([current[key] isEqual:owned[key]]) next[key] = prior[key];
    }
    return next;
}

// ipconfigd can publish e.g. DHCP-en0 as the primary instead of its preferences
// UUID. For that case select the enabled service on the same BSD interface in
// service order. A VPN's unknown service must never be replaced by another one.
static NSString *primaryService(SCPreferencesRef prefs) {
    NSDictionary *ipv4 = CFBridgingRelease(SCDynamicStoreCopyValue(NULL, CFSTR("State:/Network/Global/IPv4")));
    NSString *serviceID = ipv4[@"PrimaryService"];
    if (![serviceID isKindOfClass:NSString.class]) return nil;
    SCNetworkServiceRef service = SCNetworkServiceCopy(prefs, (__bridge CFStringRef)serviceID);
    if (service) {
        BOOL enabled = SCNetworkServiceGetEnabled(service);
        CFRelease(service);
        return enabled ? serviceID : nil;
    }
    NSString *interface = ipv4[@"PrimaryInterface"];
    if (!interface.length) return nil;
    BOOL managedByIPConfig = NO;
    for (NSString *method in @[@"DHCP", @"BOOTP", @"MANUAL", @"INFORM", @"LINKLOCAL", @"FAILOVER"]) {
        if ([serviceID caseInsensitiveCompare:[NSString stringWithFormat:@"%@-%@", method, interface]] == NSOrderedSame) managedByIPConfig = YES;
    }
    if (!managedByIPConfig) return nil;
    SCNetworkSetRef set = SCNetworkSetCopyCurrent(prefs);
    if (!set) return nil;
    NSArray *services = CFBridgingRelease(SCNetworkSetCopyServices(set));
    NSArray *order = [(__bridge NSArray *)SCNetworkSetGetServiceOrder(set) copy];
    CFRelease(set);
    NSMutableArray *matching = [NSMutableArray new];
    for (id entry in services) {
        SCNetworkServiceRef s = (__bridge SCNetworkServiceRef)entry;
        SCNetworkInterfaceRef iface = SCNetworkServiceGetInterface(s);
        if (iface && SCNetworkServiceGetEnabled(s) && [interface isEqual:(__bridge NSString *)SCNetworkInterfaceGetBSDName(iface)]) {
            [matching addObject:(__bridge NSString *)SCNetworkServiceGetServiceID(s)];
        }
    }
    for (NSString *name in order) if ([matching containsObject:name]) return name;
    return matching.count == 1 ? matching.firstObject : nil;
}

static SCNetworkProtocolRef copyProxyProtocol(SCPreferencesRef prefs, NSString *serviceID) {
    if (!serviceID) return NULL;
    SCNetworkServiceRef service = SCNetworkServiceCopy(prefs, (__bridge CFStringRef)serviceID);
    if (!service) return NULL;
    SCNetworkProtocolRef proxy = SCNetworkServiceCopyProtocol(service, kSCEntNetProxies);
    CFRelease(service);
    return proxy;
}

static BOOL restoreReceipt(SCPreferencesRef prefs, NSDictionary *receipt, NSDictionary *owned) {
    NSString *serviceID = receipt[@"service"];
    NSDictionary *prior = receipt[@"prior"];
    if (![serviceID isKindOfClass:NSString.class] || ![prior isKindOfClass:NSDictionary.class]) return NO;
    SCNetworkProtocolRef proxy = copyProxyProtocol(prefs, serviceID);
    if (proxy) {
        NSDictionary *current = (__bridge NSDictionary *)SCNetworkProtocolGetConfiguration(proxy) ?: @{};
        NSDictionary *next = restoredFields(current, prior, owned);
        BOOL ok = [next isEqual:current] || SCNetworkProtocolSetConfiguration(proxy, (__bridge CFDictionaryRef)next);
        CFRelease(proxy);
        if (!ok) return NO;
    } else if (SCError() != kSCStatusNoKey) {
        return NO;
    }
    // Keep the receipt until Apply also succeeds. A power loss after Commit
    // or an Apply failure is retried on the next service start.
    return SCPreferencesCommitChanges(prefs) && SCPreferencesApplyChanges(prefs)
        && SCPreferencesRemoveValue(prefs, receiptKey) && SCPreferencesCommitChanges(prefs);
}

static BOOL installReceipt(SCPreferencesRef prefs, NSString *serviceID, NSDictionary *owned) {
    SCNetworkProtocolRef proxy = copyProxyProtocol(prefs, serviceID);
    if (!proxy) return NO;
    NSDictionary *current = (__bridge NSDictionary *)SCNetworkProtocolGetConfiguration(proxy) ?: @{};
    NSMutableDictionary *next = [current mutableCopy];
    [next addEntriesFromDictionary:owned];
    NSDictionary *receipt = @{@"service": serviceID, @"prior": priorFields(current, owned)};
    BOOL ok = SCNetworkProtocolGetEnabled(proxy)
        && SCPreferencesSetValue(prefs, receiptKey, (__bridge CFDictionaryRef)receipt)
        && SCNetworkProtocolSetConfiguration(proxy, (__bridge CFDictionaryRef)next)
        && SCPreferencesCommitChanges(prefs) && SCPreferencesApplyChanges(prefs);
    CFRelease(proxy);
    return ok;
}

// Called only by the signed, privileged daemon. No network settings or ports
// are accepted from the IPC peer. Errors are returned to the GUI as stages.
int kdSystemProxyUpdate(int enable, int port) {
    @autoreleasepool {
        SCPreferencesRef prefs = SCPreferencesCreate(NULL, CFSTR("KeyDock Connect"), NULL);
        if (!prefs) return 2;
        NSDictionary *receipt = (__bridge NSDictionary *)SCPreferencesGetValue(prefs, receiptKey);
        NSString *active = enable ? primaryService(prefs) : nil;
        int result = 0;
        if (!receipt && !active) {
            result = enable ? 1 : 0;
        } else if (active && [active isEqual:receipt[@"service"]]) {
            SCNetworkProtocolRef proxy = copyProxyProtocol(prefs, active);
            BOOL owns = proxy && SCNetworkProtocolGetEnabled(proxy)
                && matchesOwned((__bridge NSDictionary *)SCNetworkProtocolGetConfiguration(proxy), ownedFields(port));
            if (proxy) CFRelease(proxy);
            result = owns ? 0 : 5;
        } else if (!SCPreferencesLock(prefs, false)) {
            result = 2;
        } else {
            SCPreferencesSynchronize(prefs);
            receipt = (__bridge NSDictionary *)SCPreferencesGetValue(prefs, receiptKey);
            active = enable ? primaryService(prefs) : nil;
            NSDictionary *owned = ownedFields(port);
            if (receipt && ![active isEqual:receipt[@"service"]] && !restoreReceipt(prefs, receipt, owned)) {
                result = 3;
            } else if (enable && !active) {
                result = 1;
            } else if (active && ![active isEqual:receipt[@"service"]] && !installReceipt(prefs, active, owned)) {
                result = 4;
            }
            SCPreferencesUnlock(prefs);
        }
        CFRelease(prefs);
        return result;
    }
}
