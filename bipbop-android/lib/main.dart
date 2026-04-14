import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  runApp(const BipBopApp());
}

class BipBopApp extends StatelessWidget {
  const BipBopApp({Key? key}) : super(key: key);

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'bip-bop',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        brightness: Brightness.dark,
        scaffoldBackgroundColor: const Color(0xFF0D1117),
        primarySwatch: Colors.blue,
        fontFamily: 'Roboto',
      ),
      home: const VPNScreen(),
    );
  }
}

enum VPNState { disconnected, connecting, connected, disconnecting }

class VPNScreen extends StatefulWidget {
  const VPNScreen({Key? key}) : super(key: key);

  @override
  State<VPNScreen> createState() => _VPNScreenState();
}

class _VPNScreenState extends State<VPNScreen> with SingleTickerProviderStateMixin {
  static const platform = MethodChannel('com.volfheim.bipbop/vpn');

  final TextEditingController _keyController = TextEditingController();
  VPNState _vpnState = VPNState.disconnected;
  String _statusMessage = 'Нажмите для подключения';
  bool _proxyOnly = false;
  late AnimationController _pulseController;
  final List<String> _logs = [];
  final ScrollController _logScrollController = ScrollController();

  @override
  void initState() {
    super.initState();
    _loadSavedKey();
    _setupEventChannel();
    _pulseController = AnimationController(
      vsync: this,
      duration: const Duration(seconds: 2),
    )..repeat(reverse: true);
  }

  @override
  void dispose() {
    _pulseController.dispose();
    _keyController.dispose();
    _logScrollController.dispose();
    super.dispose();
  }

  Future<void> _loadSavedKey() async {
    final prefs = await SharedPreferences.getInstance();
    setState(() {
      _keyController.text = prefs.getString('smart_key') ?? '';
    });
  }

  Future<void> _saveKey(String key) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString('smart_key', key);
  }

  void _setupEventChannel() {
    platform.setMethodCallHandler((call) async {
      switch (call.method) {
        case 'onStateChanged':
          _updateState(call.arguments as String);
          break;
        case 'onLog':
          _addLog(call.arguments as String);
          break;
      }
    });
  }

  void _addLog(String message) {
    setState(() {
      _logs.add('${DateTime.now().toString().split(' ')[1].substring(0, 8)}: $message');
      if (_logs.length > 200) _logs.removeAt(0);
    });
    // Auto-scroll to bottom
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_logScrollController.hasClients) {
        _logScrollController.animateTo(
          _logScrollController.position.maxScrollExtent,
          duration: const Duration(milliseconds: 200),
          curve: Curves.easeOut,
        );
      }
    });
  }

  void _copyLogs() {
    Clipboard.setData(ClipboardData(text: _logs.join('\n')));
    ScaffoldMessenger.of(context).showSnackBar(
      const SnackBar(content: Text('Логи скопированы в буфер')),
    );
  }

  void _updateState(String stateStr) {
    setState(() {
      switch (stateStr) {
        case 'disconnected':
          _vpnState = VPNState.disconnected;
          _statusMessage = 'Отключено';
          break;
        case 'connecting':
          _vpnState = VPNState.connecting;
          _statusMessage = 'Подключение...';
          break;
        case 'connected':
          _vpnState = VPNState.connected;
          _statusMessage = 'Подключено ✓';
          break;
        case 'reconnecting':
          _vpnState = VPNState.connecting;
          _statusMessage = 'Переподключение...';
          break;
        case 'error':
          _vpnState = VPNState.disconnected;
          _statusMessage = 'Ошибка подключения ⚠';
          _addLog('СТАТУС: Ошибка подключения');
          break;
        default:
          _statusMessage = stateStr;
      }
    });
  }

  Future<void> _toggleVPN() async {
    if (_vpnState == VPNState.disconnected) {
      if (_keyController.text.trim().isEmpty) {
        ScaffoldMessenger.of(context).showSnackBar(
          const SnackBar(content: Text('Введите Smart-ключ')),
        );
        return;
      }

      await _saveKey(_keyController.text.trim());

      setState(() {
        _vpnState = VPNState.connecting;
        _statusMessage = 'Запуск...';
        _logs.clear();
        _addLog('--- Начало сессии ---');
      });

      try {
        await platform.invokeMethod('startVpn', {
          'smartKey': _keyController.text.trim(),
          'proxyOnly': _proxyOnly,
        });
      } on PlatformException catch (e) {
        _updateState('error');
        _addLog('ОШИБКА: ${e.message}');
      }
    } else {
      setState(() {
        _vpnState = VPNState.disconnecting;
        _statusMessage = 'Отключение...';
      });

      try {
        await platform.invokeMethod('stopVpn');
      } on PlatformException catch (e) {
        _addLog('Ошибка остановки: ${e.message}');
      }
    }
  }

  Color get _accentColor {
    switch (_vpnState) {
      case VPNState.connected:
        return const Color(0xFF00E676);
      case VPNState.connecting:
      case VPNState.disconnecting:
        return const Color(0xFFFFAB40);
      case VPNState.disconnected:
        return const Color(0xFF64B5F6);
    }
  }

  IconData get _statusIcon {
    switch (_vpnState) {
      case VPNState.connected:
        return Icons.shield;
      case VPNState.connecting:
      case VPNState.disconnecting:
        return Icons.sync;
      case VPNState.disconnected:
        return Icons.shield_outlined;
    }
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      body: SafeArea(
        child: Padding(
          padding: const EdgeInsets.fromLTRB(24, 16, 24, 8),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              // Header
              Row(
                mainAxisAlignment: MainAxisAlignment.spaceBetween,
                children: [
                  const Text(
                    'bip-bop',
                    style: TextStyle(
                      fontSize: 26,
                      fontWeight: FontWeight.w700,
                      letterSpacing: -0.5,
                      color: Colors.white,
                    ),
                  ),
                  Text(
                    'v4.8-RESILIENT',
                    style: TextStyle(
                      fontSize: 12,
                      color: Colors.white.withOpacity(0.3),
                    ),
                  ),
                ],
              ),

              const Spacer(),

              // Status Circle
              Center(
                child: AnimatedBuilder(
                  animation: _pulseController,
                  builder: (context, child) {
                    final scale = (_vpnState == VPNState.connecting || _vpnState == VPNState.disconnecting)
                        ? 1.0 + _pulseController.value * 0.05
                        : 1.0;
                    return Transform.scale(
                      scale: scale,
                      child: Container(
                        width: 140,
                        height: 140,
                        decoration: BoxDecoration(
                          shape: BoxShape.circle,
                          color: _accentColor.withOpacity(0.05),
                          border: Border.all(
                            color: _accentColor.withOpacity(0.3),
                            width: 2,
                          ),
                          boxShadow: [
                            BoxShadow(
                              color: _accentColor.withOpacity(0.1),
                              blurRadius: 30,
                              spreadRadius: 5,
                            ),
                          ],
                        ),
                        child: Icon(
                          _statusIcon,
                          size: 56,
                          color: _accentColor,
                        ),
                      ),
                    );
                  },
                ),
              ),

              const SizedBox(height: 24),

              // Status Text
              Text(
                _statusMessage,
                textAlign: TextAlign.center,
                style: TextStyle(
                  fontSize: 18,
                  fontWeight: FontWeight.w600,
                  color: _accentColor,
                ),
              ),

              const Spacer(),

              // Smart Key Input
              Container(
                decoration: BoxDecoration(
                  color: const Color(0xFF161B22),
                  borderRadius: BorderRadius.circular(12),
                  border: Border.all(color: Colors.white.withOpacity(0.05)),
                ),
                child: TextField(
                  controller: _keyController,
                  style: const TextStyle(color: Colors.white, fontSize: 14),
                  decoration: InputDecoration(
                    labelText: 'Smart-ключ',
                    labelStyle: TextStyle(color: Colors.white.withOpacity(0.4)),
                    border: InputBorder.none,
                    contentPadding: const EdgeInsets.symmetric(horizontal: 16, vertical: 14),
                    prefixIcon: Icon(Icons.vpn_key_outlined, size: 20, color: Colors.white.withOpacity(0.3)),
                  ),
                  enabled: _vpnState == VPNState.disconnected,
                ),
              ),

              const SizedBox(height: 12),

              Theme(
                data: ThemeData(brightness: Brightness.dark),
                child: CheckboxListTile(
                  title: const Text(
                    "Только прокси (без VPN слота)",
                    style: TextStyle(fontSize: 14, color: Colors.white70),
                  ),
                  subtitle: const Text(
                    "Позволяет использовать другой VPN поверх",
                    style: TextStyle(fontSize: 11, color: Colors.white38),
                  ),
                  value: _proxyOnly,
                  onChanged: _vpnState == VPNState.disconnected 
                    ? (val) => setState(() => _proxyOnly = val ?? false) 
                    : null,
                  activeColor: _accentColor,
                  contentPadding: EdgeInsets.zero,
                  controlAffinity: ListTileControlAffinity.leading,
                ),
              ),

              const SizedBox(height: 12),

              // Connect Button
              SizedBox(
                height: 54,
                child: ElevatedButton(
                  onPressed: (_vpnState == VPNState.disconnecting) ? null : _toggleVPN,
                  style: ElevatedButton.styleFrom(
                    backgroundColor: _vpnState == VPNState.disconnected 
                        ? const Color(0xFF238636) 
                        : const Color(0xFFDA3633),
                    shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(12)),
                    elevation: 0,
                  ),
                  child: Text(
                    _vpnState == VPNState.disconnected ? 'Подключиться' : 'Отключить',
                    style: const TextStyle(fontSize: 16, fontWeight: FontWeight.bold, color: Colors.white),
                  ),
                ),
              ),

              const SizedBox(height: 20),

              // Logs Panel
              Expanded(
                flex: 3,
                child: Container(
                  decoration: BoxDecoration(
                    color: Colors.black.withOpacity(0.3),
                    borderRadius: BorderRadius.circular(10),
                    border: Border.all(color: Colors.white.withOpacity(0.05)),
                  ),
                  child: Column(
                    children: [
                      Padding(
                        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
                        child: Row(
                          mainAxisAlignment: MainAxisAlignment.spaceBetween,
                          children: [
                            const Text('ЛОГИ СИСТЕМЫ', style: TextStyle(fontSize: 10, fontWeight: FontWeight.bold, color: Colors.grey)),
                            GestureDetector(
                              onTap: _copyLogs,
                              child: const Text('КОПИРОВАТЬ', style: TextStyle(fontSize: 10, color: Colors.blueAccent, fontWeight: FontWeight.bold)),
                            ),
                          ],
                        ),
                      ),
                      const Divider(height: 1, color: Colors.white12),
                      Expanded(
                        child: ListView.builder(
                          controller: _logScrollController,
                          padding: const EdgeInsets.all(8),
                          itemCount: _logs.length,
                          itemBuilder: (context, index) {
                            return Padding(
                              padding: const EdgeInsets.only(bottom: 2),
                              child: Text(
                                _logs[index],
                                style: const TextStyle(
                                  fontFamily: 'monospace',
                                  fontSize: 10,
                                  color: Colors.white70,
                                ),
                              ),
                            );
                          },
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
