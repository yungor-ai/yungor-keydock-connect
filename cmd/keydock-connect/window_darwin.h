#ifndef KEYDOCK_WINDOW_DARWIN_H
#define KEYDOCK_WINDOW_DARWIN_H

enum {
    KD_CONNECT = 1,
    KD_PROXY,
    KD_RESTORE,
    KD_EXPORT,
    KD_RECHECK_ACCESS
};

enum {
    KD_CHECK_DEVICE,
    KD_CHECK_CODEX,
    KD_CHECK_MODEL,
    KD_CHECK_PROXY,
    KD_CHECK_GOOGLE,
    KD_CHECK_EGRESS
};

enum {
    KD_PROXY_INACTIVE,
    KD_PROXY_SYSTEM,
    KD_PROXY_TUN
};

void kdRunWindow(void);
void kdShowError(const char *message);
void kdReopenWindow(void);
void kdSetState(const char *server, const char *device, const char *model,
                int paired, int restorable, int subscription, int proxyMode,
                const char *updatedAt, int refreshFailed);
void kdSetCheck(int row, const char *status, const char *message, const char *hint);
void kdSetProxyApprovalProgress(int waiting);
void kdFinishOperation(const char *message, int failed);
void kdOfferUpdate(const char *version);
void kdSetUpdateProgress(const char *message);
void kdRequestUpdateTermination(void);
void kdFinishTermination(const char *failure);
#endif
