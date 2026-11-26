#ifndef KEYDOCK_WINDOW_DARWIN_H
#define KEYDOCK_WINDOW_DARWIN_H

enum {
    KD_CONNECT = 1,
    KD_PROXY,
    KD_OPEN_CODEX,
    KD_RESTORE,
    KD_EXPORT
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
    KD_PROXY_LOCAL,
    KD_PROXY_TUN
};

void kdRunWindow(void);
void kdShowError(const char *message);
void kdReopenWindow(void);
void kdSetState(const char *server, const char *device, const char *model,
                int paired, int configured, int restorable, int subscription, int proxyMode,
                const char *address, const char *updatedAt, int refreshFailed);
void kdSetCheck(int row, const char *status, const char *message, const char *hint);
void kdFinishOperation(const char *message, int failed);
void kdFinishTermination(const char *failure);
#endif
